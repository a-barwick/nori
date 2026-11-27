import Foundation

public enum NoriDateFormatting: Sendable {

    private final class FormatterCache: @unchecked Sendable {
        static let shared = FormatterCache()

        private let lock = NSLock()
        private let posixLocale = Locale(identifier: "en_US_POSIX")

        private lazy var dateOnlyFormatter: DateFormatter = {
            let f = DateFormatter()
            f.locale = posixLocale
            f.dateFormat = "yyyy-MM-dd"
            return f
        }()

        private lazy var isoFractionalFormatter: ISO8601DateFormatter = {
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            return f
        }()

        private lazy var isoStandardFormatter: ISO8601DateFormatter = {
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime]
            return f
        }()

        private lazy var shortTimeFormatter: DateFormatter = {
            let f = DateFormatter()
            f.dateStyle = .none
            f.timeStyle = .short
            return f
        }()

        private lazy var monthDaySameYearFormatter: DateFormatter = {
            let f = DateFormatter()
            return f
        }()
        private var monthDaySameYearLocaleID: String?

        private lazy var monthDayWithYearFormatter: DateFormatter = {
            let f = DateFormatter()
            return f
        }()
        private var monthDayWithYearLocaleID: String?

        func parseDateOnly(_ value: String) -> Date? {
            let currentCal = Calendar.current
            let currentTZ = TimeZone.current
            return lock.withLock {
                if dateOnlyFormatter.calendar != currentCal {
                    dateOnlyFormatter.calendar = currentCal
                }
                if dateOnlyFormatter.timeZone != currentTZ {
                    dateOnlyFormatter.timeZone = currentTZ
                }
                return dateOnlyFormatter.date(from: value)
            }
        }

        func parseISO8601Fallback(_ value: String) -> Date? {
            let direct: Date? = lock.withLock {
                if let date = isoFractionalFormatter.date(from: value) {
                    return date
                }
                if let date = isoStandardFormatter.date(from: value) {
                    return date
                }
                return nil
            }
            if let direct {
                return direct
            }
            if let date = try? Date.ISO8601FormatStyle(includingFractionalSeconds: true).parse(value) {
                return date
            }
            if let normalized = NoriDateFormatting.normalizeFractionalSeconds(value) {
                return lock.withLock {
                    isoFractionalFormatter.date(from: normalized)
                }
            }
            return nil
        }

        func formatTime(_ date: Date) -> String {
            let currentCal = Calendar.current
            let currentLocale = Locale.current
            let currentTZ = TimeZone.current
            return lock.withLock {
                if shortTimeFormatter.calendar != currentCal {
                    shortTimeFormatter.calendar = currentCal
                }
                if shortTimeFormatter.locale != currentLocale {
                    shortTimeFormatter.locale = currentLocale
                }
                if shortTimeFormatter.timeZone != currentTZ {
                    shortTimeFormatter.timeZone = currentTZ
                }
                return shortTimeFormatter.string(from: date)
            }
        }

        func formatMonthDay(_ date: Date, sameYear: Bool, calendar: Calendar) -> String {
            let currentLocale = Locale.current
            let currentTZ = TimeZone.current
            let localeID = currentLocale.identifier
            return lock.withLock {
                let formatter: DateFormatter
                if sameYear {
                    formatter = monthDaySameYearFormatter
                    if monthDaySameYearLocaleID != localeID {
                        formatter.locale = currentLocale
                        formatter.setLocalizedDateFormatFromTemplate("MMM d")
                        monthDaySameYearLocaleID = localeID
                    }
                } else {
                    formatter = monthDayWithYearFormatter
                    if monthDayWithYearLocaleID != localeID {
                        formatter.locale = currentLocale
                        formatter.setLocalizedDateFormatFromTemplate("MMM d, yyyy")
                        monthDayWithYearLocaleID = localeID
                    }
                }
                if formatter.calendar != calendar {
                    formatter.calendar = calendar
                }
                if formatter.timeZone != currentTZ {
                    formatter.timeZone = currentTZ
                }
                return formatter.string(from: date)
            }
        }
    }

    public static func parse(_ isoString: String) -> Date? {
        let trimmed = trimWhitespace(isoString)
        guard !trimmed.isEmpty else { return nil }

        if isDateOnly(trimmed) {
            return FormatterCache.shared.parseDateOnly(trimmed)
        }

        if let fast = parseRFC3339Fast(trimmed) {
            return fast
        }

        return FormatterCache.shared.parseISO8601Fallback(trimmed)
    }


    public static func relative(_ isoString: String, now: Date = Date()) -> String {
        let trimmed = trimWhitespace(isoString)
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


    public static func shortDateTime(_ isoString: String, now: Date = Date()) -> String {
        let trimmed = trimWhitespace(isoString)
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

    public static func reminderDateTime(_ isoString: String, now: Date = Date()) -> String {
        let trimmed = trimWhitespace(isoString)
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

    public static func shortTime(_ isoString: String) -> String {
        guard let date = parse(isoString) else { return isoString }
        return formatTime(date)
    }

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


    private static func trimWhitespace(_ value: String) -> String {
        let utf8 = value.utf8
        guard let first = utf8.first, let last = utf8.last else { return value }
        if first > 0x20 && first < 0x80 && last > 0x20 && last < 0x80 {
            return value
        }
        return value.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private static func isDateOnly(_ value: String) -> Bool {
        let utf8 = value.utf8
        guard utf8.count == 10 else { return false }
        var idx = utf8.startIndex
        for pos in 0..<10 {
            let b = utf8[idx]
            if pos == 4 || pos == 7 {
                if b != UInt8(ascii: "-") { return false }
            } else {
                if b < UInt8(ascii: "0") || b > UInt8(ascii: "9") { return false }
            }
            utf8.formIndex(after: &idx)
        }
        return true
    }

    /// Zero-allocation RFC3339 / ISO8601 timestamp parser for standard formats:
    /// `YYYY-MM-DDTHH:MM:SS[.fff...](Z|+HH:MM|-HH:MM|+HHMM|-HHMM)`
    private static func parseRFC3339Fast(_ value: String) -> Date? {
        var mutable = value
        return mutable.withUTF8 { buf in
            let count = buf.count
            // Minimum valid length: "YYYY-MM-DDTHH:MM:SSZ" (20 bytes)
            guard count >= 20 else { return nil }

            // Separators: '-', '-', ('T'|'t'), ':', ':'
            guard buf[4] == 0x2D, buf[7] == 0x2D,
                  (buf[10] == 0x54 || buf[10] == 0x74),
                  buf[13] == 0x3A, buf[16] == 0x3A else {
                return nil
            }

            @inline(__always)
            func digit(_ offset: Int) -> Int? {
                let d = Int(buf[offset]) &- 0x30
                return (d >= 0 && d <= 9) ? d : nil
            }

            @inline(__always)
            func parse2(_ offset: Int) -> Int? {
                guard let a = digit(offset), let b = digit(offset + 1) else { return nil }
                return a * 10 + b
            }

            @inline(__always)
            func parse4(_ offset: Int) -> Int? {
                guard let hi = parse2(offset), let lo = parse2(offset + 2) else { return nil }
                return hi * 100 + lo
            }

            guard let year = parse4(0),
                  let month = parse2(5), month >= 1, month <= 12,
                  let day = parse2(8), day >= 1,
                  let hour = parse2(11), hour <= 23,
                  let minute = parse2(14), minute <= 59,
                  let second = parse2(17), second <= 59 else {
                return nil
            }

            let maxDay: Int
            switch month {
            case 1, 3, 5, 7, 8, 10, 12: maxDay = 31
            case 4, 6, 9, 11: maxDay = 30
            case 2:
                let isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0)
                maxDay = isLeap ? 29 : 28
            default:
                return nil
            }
            guard day <= maxDay else { return nil }

            var pos = 19
            var fractionalSeconds: Double = 0

            if pos < count && buf[pos] == 0x2E { // '.'
                pos += 1
                var fracDigits = 0
                var fracValue: Int64 = 0
                var scale: Double = 1.0
                while pos < count, let d = digit(pos) {
                    if fracDigits < 9 {
                        fracValue = fracValue * 10 + Int64(d)
                        scale *= 10.0
                    }
                    fracDigits += 1
                    pos += 1
                }
                guard fracDigits > 0 else { return nil }
                fractionalSeconds = Double(fracValue) / scale
            }

            guard pos < count else { return nil }
            let offsetSeconds: Int
            let tzByte = buf[pos]
            if tzByte == 0x5A || tzByte == 0x7A { // 'Z' or 'z'
                pos += 1
                guard pos == count else { return nil }
                offsetSeconds = 0
            } else if tzByte == 0x2B || tzByte == 0x2D { // '+' or '-'
                let sign = (tzByte == 0x2B) ? 1 : -1
                pos += 1
                let remaining = count - pos
                let tzHour: Int
                let tzMinute: Int
                if remaining == 5 && buf[pos + 2] == 0x3A { // HH:MM
                    guard let h = parse2(pos), let m = parse2(pos + 3), h <= 23, m <= 59 else {
                        return nil
                    }
                    tzHour = h
                    tzMinute = m
                } else if remaining == 4 { // HHMM
                    guard let h = parse2(pos), let m = parse2(pos + 2), h <= 23, m <= 59 else {
                        return nil
                    }
                    tzHour = h
                    tzMinute = m
                } else {
                    return nil
                }
                offsetSeconds = sign * (tzHour * 3600 + tzMinute * 60)
            } else {
                return nil
            }

            // Howard Hinnant's civil-from-days inverse (proleptic Gregorian calendar, days since 1970-01-01).
            let y = month <= 2 ? year - 1 : year
            let era = (y >= 0 ? y : y - 399) / 400
            let yoe = y - era * 400
            let doy = (153 * (month + (month > 2 ? -3 : 9)) + 2) / 5 + day - 1
            let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy
            let daysSince1970 = era * 146_097 + doe - 719_468

            let wholeSeconds = Int64(daysSince1970) * 86_400
                + Int64(hour) * 3_600
                + Int64(minute) * 60
                + Int64(second)
                - Int64(offsetSeconds)
            return Date(timeIntervalSince1970: Double(wholeSeconds) + fractionalSeconds)
        }
    }

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
        FormatterCache.shared.formatTime(date)
    }

    private static func formatMonthDay(_ date: Date, relativeTo now: Date, calendar: Calendar) -> String {
        let sameYear = calendar.component(.year, from: date) == calendar.component(.year, from: now)
        return FormatterCache.shared.formatMonthDay(date, sameYear: sameYear, calendar: calendar)
    }
}
