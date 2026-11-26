#!/usr/bin/env bash
set -euo pipefail

# Upload iOS dSYMs and register a Sentry release for com.nori.ios.
#
# Usage:
#   SENTRY_ORG=my-org SENTRY_PROJECT_IOS=nori-ios SENTRY_AUTH_TOKEN=... \
#     ./scripts/upload-ios-dsyms.sh /path/to/Nori.xcarchive [version] [build]
#
# If [version] or [build] are omitted, they are read from the archive's Info.plist.

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <path-to-xcarchive-or-dsym-dir> [version] [build]" >&2
  exit 1
fi

TARGET_PATH="$1"
VERSION="${2:-}"
BUILD="${3:-}"

: "${SENTRY_ORG:?SENTRY_ORG is required}"
: "${SENTRY_PROJECT_IOS:?SENTRY_PROJECT_IOS is required}"
: "${SENTRY_AUTH_TOKEN:?SENTRY_AUTH_TOKEN is required}"

if ! command -v sentry-cli >/dev/null 2>&1; then
  echo "error: sentry-cli not found. Install with: brew install getsentry/tools/sentry-cli" >&2
  exit 1
fi

DSYM_PATH="$TARGET_PATH"
if [[ "$TARGET_PATH" == *.xcarchive ]]; then
  DSYM_PATH="$TARGET_PATH/dSYMs"
  APP_PLIST="$TARGET_PATH/Products/Applications/Nori.app/Info.plist"
  if [[ -f "$APP_PLIST" ]]; then
    if [[ -z "$VERSION" ]]; then
      VERSION=$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$APP_PLIST" 2>/dev/null || true)
    fi
    if [[ -z "$BUILD" ]]; then
      BUILD=$(/usr/libexec/PlistBuddy -c "Print :CFBundleVersion" "$APP_PLIST" 2>/dev/null || true)
    fi
  fi
fi

if [[ ! -d "$DSYM_PATH" ]]; then
  echo "error: dSYM directory not found at $DSYM_PATH" >&2
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
  "$DSYM_PATH"

if [[ -n "$RELEASE" ]]; then
  sentry-cli releases --org "$SENTRY_ORG" --project "$SENTRY_PROJECT_IOS" finalize "$RELEASE"
  echo "Finalized Sentry release: $RELEASE"
fi
