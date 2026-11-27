import SwiftUI
import NoriDesign
#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif

enum MarkdownBlock: Hashable {
    case spacer
    case heading(level: Int, text: String)
    case bullet(indent: Int, text: String)
    case ordered(indent: Int, marker: String, text: String)
    case blockquote(String)
    case table(headers: [String], rows: [[String]])
    case code(language: String?, code: String)
    case rule
    case paragraph(String)
}

struct MarkdownBlocksView: View {
    let markdown: String
    let bodyFont: Font
    let compact: Bool

    init(markdown: String, bodyFont: Font = NoriTypography.body, compact: Bool = false) {
        self.markdown = markdown
        self.bodyFont = bodyFont
        self.compact = compact
    }

    init(text: String, bodyFont: Font = NoriTypography.body, compact: Bool = false) {
        self.init(markdown: text, bodyFont: bodyFont, compact: compact)
    }

    private var blocks: [MarkdownBlock] {
        Self.parseBlocks(markdown)
    }

    static func parseBlocks(_ markdown: String) -> [MarkdownBlock] {
        var result: [MarkdownBlock] = []
        let normalized = markdown.replacingOccurrences(of: "\r\n", with: "\n")
        let lines = normalized.components(separatedBy: "\n")
        var i = 0

        func appendSpacerIfNeeded() {
            if let last = result.last, last != .spacer {
                result.append(.spacer)
            }
        }

        while i < lines.count {
            let rawLine = lines[i]
            let trimmed = rawLine.trimmingCharacters(in: .whitespaces)

            if trimmed.hasPrefix("```") {
                let lang = String(trimmed.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                var codeLines: [String] = []
                i += 1
                while i < lines.count {
                    if lines[i].trimmingCharacters(in: .whitespaces).hasPrefix("```") {
                        i += 1
                        break
                    }
                    codeLines.append(lines[i])
                    i += 1
                }
                result.append(.code(language: lang.isEmpty ? nil : lang, code: codeLines.joined(separator: "\n")))
                continue
            }

            if trimmed.isEmpty {
                appendSpacerIfNeeded()
                i += 1
                continue
            }

            if trimmed == "---" || trimmed == "***" || trimmed == "___" {
                result.append(.rule)
                i += 1
                continue
            }

            if let heading = parseHeading(trimmed) {
                result.append(.heading(level: heading.level, text: heading.text))
                i += 1
                continue
            }

            if trimmed.hasPrefix(">") {
                var quoteLines: [String] = []
                while i < lines.count {
                    let qTrimmed = lines[i].trimmingCharacters(in: .whitespaces)
                    guard qTrimmed.hasPrefix(">") else { break }
                    var content = String(qTrimmed.dropFirst())
                    if content.hasPrefix(" ") {
                        content.removeFirst()
                    }
                    quoteLines.append(content)
                    i += 1
                }
                result.append(.blockquote(quoteLines.joined(separator: "\n")))
                continue
            }

            if trimmed.contains("|"),
               i + 1 < lines.count,
               isTableSeparator(lines[i + 1]) {
                let headers = parseTableRow(trimmed)
                i += 2
                var rows: [[String]] = []
                while i < lines.count {
                    let rowTrimmed = lines[i].trimmingCharacters(in: .whitespaces)
                    if rowTrimmed.isEmpty || !rowTrimmed.contains("|") || rowTrimmed.hasPrefix("```") {
                        break
                    }
                    var cells = parseTableRow(rowTrimmed)
                    if cells.count < headers.count {
                        cells.append(contentsOf: Array(repeating: "", count: headers.count - cells.count))
                    } else if cells.count > headers.count {
                        cells = Array(cells.prefix(headers.count))
                    }
                    rows.append(cells)
                    i += 1
                }
                if !headers.isEmpty {
                    result.append(.table(headers: headers, rows: rows))
                    continue
                }
            }

            if let bullet = parseBullet(rawLine) {
                result.append(.bullet(indent: bullet.indent, text: bullet.text))
                i += 1
                continue
            }

            if let ordered = parseOrderedItem(rawLine) {
                result.append(.ordered(indent: ordered.indent, marker: ordered.marker, text: ordered.text))
                i += 1
                continue
            }

            var paragraphLines: [String] = [trimmed]
            i += 1
            while i < lines.count {
                let nextRaw = lines[i]
                let nextTrimmed = nextRaw.trimmingCharacters(in: .whitespaces)
                if nextTrimmed.isEmpty
                    || nextTrimmed.hasPrefix("```")
                    || nextTrimmed == "---" || nextTrimmed == "***" || nextTrimmed == "___"
                    || parseHeading(nextTrimmed) != nil
                    || nextTrimmed.hasPrefix(">")
                    || parseBullet(nextRaw) != nil
                    || parseOrderedItem(nextRaw) != nil
                    || (nextTrimmed.contains("|") && i + 1 < lines.count && isTableSeparator(lines[i + 1])) {
                    break
                }
                paragraphLines.append(nextTrimmed)
                i += 1
            }
            result.append(.paragraph(paragraphLines.joined(separator: "\n")))
        }

        if result.last == .spacer {
            result.removeLast()
        }

        return result
    }

    static func containsBlockSyntax(_ markdown: String) -> Bool {
        for block in parseBlocks(markdown) {
            switch block {
            case .paragraph, .spacer:
                continue
            case .heading, .bullet, .ordered, .blockquote, .table, .code, .rule:
                return true
            }
        }
        return false
    }

    static func extractDocumentTitle(_ markdown: String) -> (title: String?, body: String) {
        let normalized = markdown.replacingOccurrences(of: "\r\n", with: "\n")
        let lines = normalized.components(separatedBy: "\n")
        var checked = 0

        for index in 0..<lines.count {
            let trimmed = lines[index].trimmingCharacters(in: .whitespaces)
            if trimmed.isEmpty { continue }
            checked += 1
            if checked > 4 { break }

            var rawTitle: String?
            if let heading = parseHeading(trimmed), heading.level <= 3 {
                rawTitle = heading.text
            } else if let titled = matchTitlePrefix(trimmed) {
                rawTitle = titled
            } else if checked == 1, trimmed.count <= 140, trimmed.hasPrefix("**"), trimmed.hasSuffix("**"), trimmed.count > 4 {
                let inner = String(trimmed.dropFirst(2).dropLast(2))
                if !inner.contains("*") {
                    rawTitle = inner
                }
            }

            if let rawTitle {
                let cleaned = cleanTitle(rawTitle)
                if !cleaned.isEmpty {
                    let rest = lines.dropFirst(index + 1).joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
                    return (cleaned, rest)
                }
            }
            if checked == 1 { break }
        }

        return (nil, normalized.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    static func resolveDocumentPresentation(body: String, explicitTitle: String?) -> (title: String, body: String) {
        let extracted = extractDocumentTitle(body)
        if let explicit = explicitTitle?.trimmingCharacters(in: .whitespacesAndNewlines), !explicit.isEmpty {
            let matchesExtracted = extracted.title?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() == explicit.lowercased()
            return (explicit, matchesExtracted ? extracted.body : body.trimmingCharacters(in: .whitespacesAndNewlines))
        }
        if let title = extracted.title, !title.isEmpty {
            return (title, extracted.body)
        }
        let fallback = firstSentence(body)
        return (fallback.isEmpty ? "Document" : fallback, body.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    private static func cleanTitle(_ raw: String) -> String {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        while s.hasPrefix("*") || s.hasPrefix("_") || s.hasPrefix("`") {
            s.removeFirst()
        }
        while s.hasSuffix("*") || s.hasSuffix("_") || s.hasSuffix("`") {
            s.removeLast()
        }
        s = s.trimmingCharacters(in: .whitespacesAndNewlines)
        if (s.hasPrefix("\"") && s.hasSuffix("\""))
            || (s.hasPrefix("'") && s.hasSuffix("'"))
            || (s.hasPrefix("“") && s.hasSuffix("”")) {
            s = String(s.dropFirst().dropLast()).trimmingCharacters(in: .whitespacesAndNewlines)
        }
        return s
    }

    private static func matchTitlePrefix(_ line: String) -> String? {
        let lower = line.lowercased()
        let prefixes = ["**title**:", "**title:**", "title:"]
        for prefix in prefixes where lower.hasPrefix(prefix) {
            return String(line.dropFirst(prefix.count)).trimmingCharacters(in: .whitespaces)
        }
        return nil
    }

    private static func firstSentence(_ text: String) -> String {
        let stripped = text
            .replacingOccurrences(of: "#", with: "")
            .replacingOccurrences(of: "*", with: "")
            .replacingOccurrences(of: "`", with: "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard !stripped.isEmpty else { return "" }
        let firstLine = stripped.components(separatedBy: .newlines).first ?? stripped
        return String(firstLine.prefix(64)).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private static func leadingIndentLevel(_ rawLine: String) -> Int {
        var spaces = 0
        for ch in rawLine {
            if ch == " " {
                spaces += 1
            } else if ch == "\t" {
                spaces += 2
            } else {
                break
            }
        }
        return min(4, spaces / 2)
    }

    private static func parseHeading(_ trimmed: String) -> (level: Int, text: String)? {
        if trimmed.hasPrefix("#### ") {
            return (4, String(trimmed.dropFirst(5)).trimmingCharacters(in: .whitespaces))
        }
        if trimmed.hasPrefix("### ") {
            return (3, String(trimmed.dropFirst(4)).trimmingCharacters(in: .whitespaces))
        }
        if trimmed.hasPrefix("## ") {
            return (2, String(trimmed.dropFirst(3)).trimmingCharacters(in: .whitespaces))
        }
        if trimmed.hasPrefix("# ") {
            return (1, String(trimmed.dropFirst(2)).trimmingCharacters(in: .whitespaces))
        }
        return nil
    }

    private static func parseBullet(_ rawLine: String) -> (indent: Int, text: String)? {
        let indent = leadingIndentLevel(rawLine)
        let trimmed = rawLine.trimmingCharacters(in: .whitespaces)
        if trimmed.hasPrefix("- ") || trimmed.hasPrefix("* ") || trimmed.hasPrefix("+ ") {
            return (indent, String(trimmed.dropFirst(2)))
        }
        return nil
    }

    private static func parseOrderedItem(_ rawLine: String) -> (indent: Int, marker: String, text: String)? {
        let indent = leadingIndentLevel(rawLine)
        let trimmed = rawLine.trimmingCharacters(in: .whitespaces)
        guard let dotIndex = trimmed.firstIndex(of: "."), dotIndex > trimmed.startIndex else {
            return nil
        }
        let numberPart = trimmed[..<dotIndex]
        guard numberPart.allSatisfy(\.isNumber) else { return nil }
        let afterDot = trimmed.index(after: dotIndex)
        guard afterDot < trimmed.endIndex, trimmed[afterDot] == " " else { return nil }
        let text = String(trimmed[trimmed.index(after: afterDot)...]).trimmingCharacters(in: .whitespaces)
        return (indent, "\(numberPart).", text)
    }

    private static func isTableSeparator(_ rawLine: String) -> Bool {
        let trimmed = rawLine.trimmingCharacters(in: .whitespaces)
        guard trimmed.contains("|") && trimmed.contains("-") else { return false }
        let cells = parseTableRow(trimmed)
        guard !cells.isEmpty else { return false }
        return cells.allSatisfy { cell in
            let cleaned = cell.trimmingCharacters(in: .whitespaces)
            guard !cleaned.isEmpty else { return false }
            let core = cleaned.trimmingCharacters(in: CharacterSet(charactersIn: ":"))
            return !core.isEmpty && core.allSatisfy { $0 == "-" }
        }
    }

    private static func parseTableRow(_ rawLine: String) -> [String] {
        var trimmed = rawLine.trimmingCharacters(in: .whitespaces)
        if trimmed.hasPrefix("|") { trimmed.removeFirst() }
        if trimmed.hasSuffix("|") { trimmed.removeLast() }
        return trimmed
            .split(separator: "|", omittingEmptySubsequences: false)
            .map { $0.trimmingCharacters(in: .whitespaces) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: compact ? 6 : 8) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                switch block {
                case .spacer:
                    Spacer().frame(height: compact ? 1 : 2)
                case .rule:
                    Rectangle()
                        .fill(NoriColors.hairline)
                        .frame(height: 1)
                        .padding(.vertical, 4)
                case .heading(let level, let text):
                    inlineMarkdownText(text)
                        .font(headingFont(for: level))
                        .foregroundStyle(NoriColors.ink)
                        .padding(.top, level == 1 ? 0 : 4)
                        .fixedSize(horizontal: false, vertical: true)
                case .bullet(let indent, let text):
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(indent % 2 == 0 ? "•" : "◦")
                            .font(.system(size: 14, weight: .bold))
                            .foregroundStyle(indent == 0 ? NoriColors.accent : NoriColors.ink3)
                        inlineMarkdownText(text)
                            .font(bodyFont)
                            .foregroundStyle(NoriColors.ink)
                            .lineSpacing(2.5)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .padding(.leading, CGFloat(indent) * 16)
                case .ordered(let indent, let marker, let text):
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Text(marker)
                            .font(NoriTypography.metaSemibold)
                            .monospacedDigit()
                            .foregroundStyle(NoriColors.ink2)
                            .frame(minWidth: 18, alignment: .trailing)
                        inlineMarkdownText(text)
                            .font(bodyFont)
                            .foregroundStyle(NoriColors.ink)
                            .lineSpacing(2.5)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .padding(.leading, CGFloat(indent) * 16)
                case .blockquote(let text):
                    HStack(alignment: .top, spacing: 10) {
                        RoundedRectangle(cornerRadius: 1.5, style: .continuous)
                            .fill(NoriColors.hairlineStrong)
                            .frame(width: 2.5)
                        inlineMarkdownText(text)
                            .font(bodyFont)
                            .foregroundStyle(NoriColors.ink2)
                            .lineSpacing(2.5)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .padding(.vertical, 2)
                case .table(let headers, let rows):
                    MarkdownTableView(headers: headers, rows: rows)
                case .code(let language, let code):
                    MarkdownCodeBlockView(language: language, code: code)
                case .paragraph(let text):
                    inlineMarkdownText(text)
                        .font(bodyFont)
                        .foregroundStyle(NoriColors.ink)
                        .lineSpacing(2.5)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .tint(NoriColors.accent)
    }

    private func headingFont(for level: Int) -> Font {
        switch level {
        case 1: return .system(size: 18, weight: .bold)
        case 2: return .system(size: 16.5, weight: .bold)
        case 3: return .system(size: 15, weight: .semibold)
        default: return .system(size: 14, weight: .semibold)
        }
    }

    private func inlineMarkdownText(_ source: String) -> Text {
        if let attributed = try? AttributedString(
            markdown: source,
            options: AttributedString.MarkdownParsingOptions(
                interpretedSyntax: .inlineOnlyPreservingWhitespace
            )
        ) {
            return Text(attributed)
        }
        return Text(source)
    }
}

private struct MarkdownCodeBlockView: View {
    let language: String?
    let code: String

    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                if let language, !language.isEmpty {
                    Text(language)
                        .font(NoriTypography.monoSmall)
                        .foregroundStyle(NoriColors.ink3)
                }
                Spacer(minLength: 8)
                Button(action: copyCode) {
                    HStack(spacing: 4) {
                        Image(systemName: copied ? "checkmark" : "doc.on.doc")
                            .font(.system(size: 11, weight: .semibold))
                        Text(copied ? "Copied" : "Copy")
                            .font(.system(size: 11.5, weight: .medium))
                    }
                    .foregroundStyle(copied ? NoriColors.accent : NoriColors.ink2)
                    .padding(.horizontal, 8)
                    .frame(height: 24)
                    .background(NoriColors.surface, in: Capsule())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(copied ? "Copied code" : "Copy code")
            }

            ScrollView(.horizontal, showsIndicators: false) {
                Text(code)
                    .font(NoriTypography.monoSmall)
                    .foregroundStyle(NoriColors.ink)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .tint(NoriColors.accent)
            }
        }
        .padding(10)
        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
    }

    private func copyCode() {
        #if os(iOS)
        UIPasteboard.general.string = code
        #elseif os(macOS)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(code, forType: .string)
        #endif
        copied = true
        Task {
            try? await Task.sleep(for: .seconds(1.6))
            copied = false
        }
    }
}

private struct MarkdownTableView: View {
    let headers: [String]
    let rows: [[String]]

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 0) {
                GridRow {
                    ForEach(Array(headers.enumerated()), id: \.offset) { _, header in
                        inlineCell(header)
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink2)
                            .padding(.vertical, 6)
                    }
                }

                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
                    .gridCellUnsizedAxes(.horizontal)

                ForEach(Array(rows.enumerated()), id: \.offset) { rowIndex, row in
                    GridRow {
                        ForEach(Array(row.enumerated()), id: \.offset) { _, cell in
                            inlineCell(cell)
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink)
                                .padding(.vertical, 6)
                        }
                    }

                    if rowIndex < rows.count - 1 {
                        Rectangle()
                            .fill(NoriColors.hairline)
                            .frame(height: 1)
                            .gridCellUnsizedAxes(.horizontal)
                    }
                }
            }
        }
        .padding(.vertical, 2)
    }

    private func inlineCell(_ source: String) -> Text {
        if let attributed = try? AttributedString(
            markdown: source,
            options: AttributedString.MarkdownParsingOptions(
                interpretedSyntax: .inlineOnlyPreservingWhitespace
            )
        ) {
            return Text(attributed)
        }
        return Text(source)
    }
}
