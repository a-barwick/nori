#!/usr/bin/env bash
set -euo pipefail

# Upload iOS dSYMs and register a Sentry release for com.nori.ios.
#
# Usage:
#   SENTRY_ORG=my-org SENTRY_PROJECT_IOS=nori-ios SENTRY_AUTH_TOKEN=... \
#     ./scripts/upload-ios-dsyms.sh /path/to/Nori.xcarchive [version] [build]
#
# If [version] or [build] are omitted, they are read from the archived app.

VERIFY_ONLY=0
if [[ "${1:-}" == "--verify-only" ]]; then
  VERIFY_ONLY=1
  shift
fi
if [[ $# -lt 1 || $# -gt 3 ]]; then
  echo "Usage: $0 <path-to-xcarchive-or-dsym-dir> [version] [build]" >&2
  echo "       $0 --verify-only <path-to-xcarchive>" >&2
  exit 1
fi

TARGET_PATH="$1"
VERSION="${2:-}"
BUILD="${3:-}"

DSYM_PATH="$TARGET_PATH"
if [[ "$TARGET_PATH" == *.xcarchive ]]; then
  DSYM_PATH="$TARGET_PATH/dSYMs"
  APP="$TARGET_PATH/Products/Applications/Nori.app"
  APP_PLIST="$APP/Info.plist"
  uuid_list() {
    xcrun dwarfdump --uuid "$1" | awk '/^UUID:/ { print $2, $3 }' | sort
  }
  APP_UUIDS=$(uuid_list "$APP/Nori")
  DSYM_UUIDS=$(uuid_list "$DSYM_PATH/Nori.app.dSYM")
  if [[ -z "$APP_UUIDS" || "$APP_UUIDS" != "$DSYM_UUIDS" ]]; then
    echo "error: the archive's Nori dSYM does not match its app binary" >&2
    exit 1
  fi
  echo "Verified Nori dSYM UUIDs against the archived app"

  APP_DEPENDENCIES=$(xcrun otool -L "$APP/Nori")
  if [[ "$APP_DEPENDENCIES" == *'@rpath/Sentry.framework/Sentry'* ]]; then
    SENTRY_BINARY="$APP/Frameworks/Sentry.framework/Sentry"
    SENTRY_DSYM="$DSYM_PATH/Sentry.framework.dSYM"
    if [[ ! -f "$SENTRY_BINARY" || ! -d "$SENTRY_DSYM" ]]; then
      echo "error: the archive is missing the linked Sentry framework or its dSYM" >&2
      exit 1
    fi
    SENTRY_UUIDS=$(uuid_list "$SENTRY_BINARY")
    SENTRY_DSYM_UUIDS=$(uuid_list "$SENTRY_DSYM")
    if [[ -z "$SENTRY_UUIDS" || -n "$(comm -23 <(printf '%s\n' "$SENTRY_UUIDS") <(printf '%s\n' "$SENTRY_DSYM_UUIDS"))" ]]; then
      echo "error: the archive's Sentry dSYM does not cover every linked Sentry architecture" >&2
      exit 1
    fi
    echo "Verified Sentry dSYM UUIDs against the archived framework"
  fi

  if [[ "$VERIFY_ONLY" == 1 ]]; then
    exit 0
  fi
  if [[ -f "$APP_PLIST" ]]; then
    if [[ -z "$VERSION" ]]; then
      VERSION=$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$APP_PLIST" 2>/dev/null || true)
    fi
    if [[ -z "$BUILD" ]]; then
      BUILD=$(/usr/libexec/PlistBuddy -c "Print :CFBundleVersion" "$APP_PLIST" 2>/dev/null || true)
    fi
  fi
elif [[ "$VERIFY_ONLY" == 1 ]]; then
  echo "error: --verify-only requires a .xcarchive" >&2
  exit 1
fi

if [[ ! -d "$DSYM_PATH" ]]; then
  echo "error: dSYM directory not found at $DSYM_PATH" >&2
  exit 1
fi

: "${SENTRY_ORG:?SENTRY_ORG is required}"
: "${SENTRY_PROJECT_IOS:?SENTRY_PROJECT_IOS is required}"

export PATH="$PATH:/opt/homebrew/bin:/usr/local/bin"
if ! command -v sentry-cli >/dev/null 2>&1; then
  echo "error: sentry-cli not found. Install with: brew install --cask sentry-cli" >&2
  exit 1
fi

RELEASE=""
if [[ -n "$VERSION" && -n "$BUILD" ]]; then
  RELEASE="com.nori.ios@${VERSION}+${BUILD}"
  echo "Registering Sentry release: $RELEASE"
  sentry-cli releases --org "$SENTRY_ORG" --project "$SENTRY_PROJECT_IOS" new "$RELEASE"
fi

echo "Uploading dSYMs from $DSYM_PATH..."
sentry-cli debug-files upload \
  --org "$SENTRY_ORG" \
  --project "$SENTRY_PROJECT_IOS" \
  --wait \
  "$DSYM_PATH"

if [[ -n "$RELEASE" ]]; then
  sentry-cli releases --org "$SENTRY_ORG" --project "$SENTRY_PROJECT_IOS" finalize "$RELEASE"
  echo "Finalized Sentry release: $RELEASE"
fi
