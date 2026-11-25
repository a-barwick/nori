import Foundation

/// Swift 6 concurrency-safe formatting utilities for ISO-8601 timestamps and
/// date-only strings (`yyyy-MM-dd`) returned by the Nori backend.
public enum NoriDateFormatting: Sendable {
    // MARK: - Parsing

    /// Parses ISO-8601 timestamps (with or without fractional seconds, including
    /// microsecond/nanosecond precision) as well as date-only `yyyy-MM-dd` strings
    /// in the user's current calendar/time zone.
    public static func parse(_ isoString: String) -> Date? {
        let trimmed = isoString.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }

        if isDateOnly(trimmed) {
            let formatter = DateFormatter()
            formatter.calendar = Calendar.current
            formatter.locale = Locale(identifier: "en_US_POSIX")
            formatter.timeZone = TimeZone.current
            formatter.dateFormat = "yyyy-MM-dd"
            return formatter.date(from: trimmed)
        }

        let fractionalFormatter = ISO8601DateFormatter()
        fractionalFormatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractionalFormatter.date(from: trimmed) {
            return date
        }

        let standardFormatter = ISO8601DateFormatter()
        standardFormatter.formatOptions = [.withInternetDateTime]
        if let date = standardFormatter.date(from: trimmed) {
            return date
        }

        if let date = try? Date.ISO8601FormatStyle(includingFractionalSeconds: true).parse(trimmed) {
            return date
        }

        if let normalized = normalizeFractionalSeconds(trimmed),
           let date = fractionalFormatter.date(from: normalized) {
            return date
        }

        return nil
    }

    // MARK: - Relative

    /// Human-readable relative string for both past and future timestamps
    /// (e.g. `"Just now"`, `"5m ago"`, `"2h ago"`, `"Yesterday"`, `"3d ago"`,
    /// `"Nov 24"`, or `"in 5m"`, `"in 2h"`, `"Tomorrow"`).
    /// Falls back gracefully to the original string if unparseable.
    public static func relative(_ isoString: String, now: Date = Date()) -> String {
        let trimmed = isoString.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let date = parse(trimmed) else { return isoString }

        let calendar = Calendar.current

        if isDateOnly(trimmed) {
            return relativeCalendarDay(for: date, now: now, calendar: calendar)
        }

        let delta = date.timeIntervalSince(now)

        if delta <= 0 {
            let elapsed = -delta
            if elapsed < 45 {
                return "Just now"
            }
            if elapsed < 3600 {
                let mins = max(1, Int((elapsed / 60).rounded()))
                return mins >= 60 ? "1h ago" : "\(mins)m ago"
            }
            if elapsed < 86_400 {
                let isSameDay = calendar.isDate(date, inSameDayAs: now)
                if isSameDay || elapsed < 12 * 3600 {
                    let hours = max(1, Int(elapsed / 3600))
                    return "\(hours)h ago"
                }
                return "Yesterday"
            }

            let days = calendarDayDelta(from: date, to: now, calendar: calendar)
            if days == 1 {
                return "Yesterday"
            }
            if days > 1 && days < 7 {
                return "\(days)d ago"
            }
            return formatMonthDay(date, relativeTo: now, calendar: calendar)
        } else {
            if delta < 45 {
                return "in 1m"
            }
            if delta < 3600 {
                let mins = max(1, Int((delta / 60).rounded()))
                return mins >= 60 ? "in 1h" : "in \(mins)m"
            }
            if delta < 86_400 {
                let isSameDay = calendar.isDate(date, inSameDayAs: now)
                if isSameDay || delta < 12 * 3600 {
                    let hours = max(1, Int(delta / 3600))
                    return "in \(hours)h"
                }
                return "Tomorrow"
            }

            let days = calendarDayDelta(from: now, to: date, calendar: calendar)
            if days == 1 {
                return "Tomorrow"
            }
            if days > 1 && days < 7 {
                return "in \(days)d"
            }
            return formatMonthDay(date, relativeTo: now, calendar: calendar)
        }
    }

    // MARK: - Short Date & Time

    /// Concise readable date and time (e.g. `"2:15 PM"` if today,
    /// `"Yesterday, 2:15 PM"`, `"Tomorrow, 2:15 PM"`, `"Nov 24, 2:15 PM"`,
    /// or with year if in a different year).
    public static func shortDateTime(_ isoString: String, now: Date = Date()) -> String {
        let trimmed = isoString.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let date = parse(trimmed) else { return isoString }

        if isDateOnly(trimmed) {
            return shortDate(trimmed, now: now)
        }

        let calendar = Calendar.current
        let timeStr = formatTime(date)

        if calendar.isDate(date, inSameDayAs: now) {
            return timeStr
        }
        if let yesterday = calendar.date(byAdding: .day, value: -1, to: now),
           calendar.isDate(date, inSameDayAs: yesterday) {
            return "Yesterday, \(timeStr)"
        }
        if let tomorrow = calendar.date(byAdding: .day, value: 1, to: now),
           calendar.isDate(date, inSameDayAs: tomorrow) {
            return "Tomorrow, \(timeStr)"
        }

        let dateStr = formatMonthDay(date, relativeTo: now, calendar: calendar)
        return "\(dateStr), \(timeStr)"
    }

    /// Plain-English local reminder timestamp (e.g. `"Today at 2:15 PM"`,
    /// `"Tomorrow at 12:00 PM"`, `"Nov 25 at 12:00 PM"`).
    public static func reminderDateTime(_ isoString: String, now: Date = Date()) -> String {
        let trimmed = isoString.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let date = parse(trimmed) else { return isoString }

        if isDateOnly(trimmed) {
            return shortDate(trimmed, now: now)
        }

        let calendar = Calendar.current
        let timeStr = formatTime(date)

        if calendar.isDate(date, inSameDayAs: now) {
            return "Today at \(timeStr)"
        }
        if let yesterday = calendar.date(byAdding: .day, value: -1, to: now),
           calendar.isDate(date, inSameDayAs: yesterday) {
            return "Yesterday at \(timeStr)"
        }
        if let tomorrow = calendar.date(byAdding: .day, value: 1, to: now),
           calendar.isDate(date, inSameDayAs: tomorrow) {
            return "Tomorrow at \(timeStr)"
        }

        let dateStr = formatMonthDay(date, relativeTo: now, calendar: calendar)
        return "\(dateStr) at \(timeStr)"
    }

    /// Short time string (e.g. `"2:15 PM"`).
    public static func shortTime(_ isoString: String) -> String {
        guard let date = parse(isoString) else { return isoString }
        return formatTime(date)
    }

    /// Readable date (e.g. `"Today"`, `"Yesterday"`, `"Tomorrow"`, `"Nov 24"`,
    /// or `"Nov 24, 2025"` if in a different year). Handles `yyyy-MM-dd` cleanly
    /// in the local calendar.
    public static func shortDate(_ isoString: String, now: Date = Date()) -> String {
        guard let date = parse(isoString) else { return isoString }

        let calendar = Calendar.current
        if calendar.isDate(date, inSameDayAs: now) {
            return "Today"
        }
        if let yesterday = calendar.date(byAdding: .day, value: -1, to: now),
           calendar.isDate(date, inSameDayAs: yesterday) {
            return "Yesterday"
        }
        if let tomorrow = calendar.date(byAdding: .day, value: 1, to: now),
           calendar.isDate(date, inSameDayAs: tomorrow) {
            return "Tomorrow"
        }
        return formatMonthDay(date, relativeTo: now, calendar: calendar)
    }

    // MARK: - Expiry Countdown

    /// Countdown string for expiring items (e.g. `"Expires in 12m"`,
    /// `"Expires in 2h"`, `"Expires in 3d"`, `"Expires soon"`, `"Expired"`).
    public static func expiryCountdown(_ isoString: String, now: Date = Date()) -> String {
        guard let date = parse(isoString) else { return isoString }

        let remaining = date.timeIntervalSince(now)
        if remaining <= 0 {
            return "Expired"
        }
        if remaining < 60 {
            return "Expires soon"
        }
        if remaining < 3600 {
            let mins = max(1, Int(remaining / 60))
            return "Expires in \(mins)m"
        }
        if remaining < 86_400 {
            let hours = max(1, Int(remaining / 3600))
            return "Expires in \(hours)h"
        }
        let days = max(1, Int(remaining / 86_400))
        return "Expires in \(days)d"
    }

    // MARK: - Duration

    /// Concise duration between two ISO-8601 timestamps (e.g. `"14s"`,
    /// `"2m 10s"`, `"1h 4m"`). Uses `now` when `endISO` is omitted.
    public static func duration(
        from startISO: String,
        to endISO: String? = nil,
        now: Date = Date()
    ) -> String? {
        guard let start = parse(startISO) else { return nil }
        let end: Date
        if let endISO {
            guard let parsedEnd = parse(endISO) else { return nil }
            end = parsedEnd
        } else {
            end = now
        }

        let totalSeconds = max(0, Int(end.timeIntervalSince(start).rounded()))
        if totalSeconds < 60 {
            return "\(totalSeconds)s"
        }
        if totalSeconds < 3600 {
            let mins = totalSeconds / 60
            let secs = totalSeconds % 60
            return secs == 0 ? "\(mins)m" : "\(mins)m \(secs)s"
        }
        let hours = totalSeconds / 3600
        let mins = (totalSeconds % 3600) / 60
        return mins == 0 ? "\(hours)h" : "\(hours)h \(mins)m"
    }

    // MARK: - Internal Helpers

    private static func isDateOnly(_ value: String) -> Bool {
        guard value.count == 10 else { return false }
        let bytes = Array(value.utf8)
        guard bytes[4] == UInt8(ascii: "-"), bytes[7] == UInt8(ascii: "-") else {
            return false
        }
        for idx in [0, 1, 2, 3, 5, 6, 8, 9] {
            if bytes[idx] < UInt8(ascii: "0") || bytes[idx] > UInt8(ascii: "9") {
                return false
            }
        }
        return true
    }

    /// Normalizes arbitrary-length fractional seconds (e.g. 6- or 9-digit Go
    /// RFC3339Nano timestamps) to 3 digits so `ISO8601DateFormatter` parses them.
    private static func normalizeFractionalSeconds(_ value: String) -> String? {
        guard let dotIndex = value.firstIndex(of: ".") else { return nil }
        let afterDot = value.index(after: dotIndex)
        var fracEnd = afterDot
        while fracEnd < value.endIndex, value[fracEnd].isNumber {
            fracEnd = value.index(after: fracEnd)
        }
        guard fracEnd > afterDot else { return nil }

        let frac = String(value[afterDot..<fracEnd])
        let normalizedFrac: String
        if frac.count >= 3 {
            normalizedFrac = String(frac.prefix(3))
        } else {
            normalizedFrac = frac.padding(toLength: 3, withPad: "0", startingAt: 0)
        }
        return "\(value[..<afterDot])\(normalizedFrac)\(value[fracEnd...])"
    }

    private static func calendarDayDelta(from earlier: Date, to later: Date, calendar: Calendar) -> Int {
        let start = calendar.startOfDay(for: earlier)
        let end = calendar.startOfDay(for: later)
        return calendar.dateComponents([.day], from: start, to: end).day ?? 0
    }

    private static func relativeCalendarDay(for date: Date, now: Date, calendar: Calendar) -> String {
        let days = calendarDayDelta(from: now, to: date, calendar: calendar)
        switch days {
        case 0: return "Today"
        case -1: return "Yesterday"
        case 1: return "Tomorrow"
        case -6 ... -2: return "\(-days)d ago"
        case 2 ... 6: return "in \(days)d"
        default: return formatMonthDay(date, relativeTo: now, calendar: calendar)
        }
    }

    private static func formatTime(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.calendar = Calendar.current
        formatter.locale = Locale.current
        formatter.timeZone = TimeZone.current
        formatter.dateStyle = .none
        formatter.timeStyle = .short
        return formatter.string(from: date)
    }

    private static func formatMonthDay(_ date: Date, relativeTo now: Date, calendar: Calendar) -> String {
        let sameYear = calendar.component(.year, from: date) == calendar.component(.year, from: now)
        let formatter = DateFormatter()
        formatter.calendar = calendar
        formatter.locale = Locale.current
        formatter.timeZone = TimeZone.current
        formatter.setLocalizedDateFormatFromTemplate(sameYear ? "MMM d" : "MMM d, yyyy")
        return formatter.string(from: date)
    }
}
