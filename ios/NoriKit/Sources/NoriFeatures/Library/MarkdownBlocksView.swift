import SwiftUI
import NoriDesign

struct MarkdownBlocksView: View {
    let markdown: String

    private enum Block: Hashable {
        case spacer
        case heading(level: Int, text: String)
        case bullet(text: String)
        case code(String)
        case rule
        case paragraph(String)
    }

    private var blocks: [Block] {
        var result: [Block] = []
        let lines = markdown.components(separatedBy: .newlines)
        var inCodeFence = false
        var codeLines: [String] = []

        for rawLine in lines {
            let trimmed = rawLine.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("```") {
                if inCodeFence {
                    result.append(.code(codeLines.joined(separator: "\n")))
                    codeLines.removeAll()
                    inCodeFence = false
                } else {
                    inCodeFence = true
                }
                continue
            }

            if inCodeFence {
                codeLines.append(rawLine)
                continue
            }

            if trimmed.isEmpty {
                result.append(.spacer)
            } else if trimmed == "---" || trimmed == "***" || trimmed == "___" {
                result.append(.rule)
            } else if trimmed.hasPrefix("### ") {
                result.append(.heading(level: 3, text: String(trimmed.dropFirst(4))))
            } else if trimmed.hasPrefix("## ") {
                result.append(.heading(level: 2, text: String(trimmed.dropFirst(3))))
            } else if trimmed.hasPrefix("# ") {
                result.append(.heading(level: 1, text: String(trimmed.dropFirst(2))))
            } else if trimmed.hasPrefix("- ") || trimmed.hasPrefix("* ") {
                result.append(.bullet(text: String(trimmed.dropFirst(2))))
            } else {
                result.append(.paragraph(trimmed))
            }
        }

        if inCodeFence && !codeLines.isEmpty {
            result.append(.code(codeLines.joined(separator: "\n")))
        }

        return result
    }

    var body: some View {
        LazyVStack(alignment: .leading, spacing: 8) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                switch block {
                case .spacer:
                    Spacer().frame(height: 2)
                case .rule:
                    Rectangle()
                        .fill(NoriColors.hairline)
                        .frame(height: 1)
                        .padding(.vertical, 4)
                case .heading(let level, let text):
                    inlineMarkdownText(text)
                        .font(.system(size: level == 1 ? 18 : (level == 2 ? 16.5 : 15), weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                        .padding(.top, level == 1 ? 0 : 4)
                case .bullet(let text):
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text("•")
                            .font(.system(size: 14, weight: .bold))
                            .foregroundStyle(NoriColors.accent)
                        inlineMarkdownText(text)
                            .font(.system(size: 14))
                            .foregroundStyle(NoriColors.ink)
                            .lineSpacing(3)
                    }
                case .code(let code):
                    ScrollView(.horizontal, showsIndicators: false) {
                        Text(code)
                            .font(NoriTypography.monoSmall)
                            .foregroundStyle(NoriColors.ink)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .tint(NoriColors.accent)
                    }
                    .padding(10)
                    .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                    .overlay(
                        RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                            .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                    )
                case .paragraph(let text):
                    inlineMarkdownText(text)
                        .font(.system(size: 14))
                        .foregroundStyle(NoriColors.ink)
                        .lineSpacing(3)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .tint(NoriColors.accent)
    }

    private func inlineMarkdownText(_ source: String) -> Text {
        if let attributed = try? AttributedString(markdown: source) {
            return Text(attributed)
        }
        return Text(source)
    }
}
