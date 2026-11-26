import SwiftUI
#if canImport(UniformTypeIdentifiers)
import UniformTypeIdentifiers
#endif
#if canImport(UIKit)
import UIKit
typealias DocumentPlatformImage = UIImage
#elseif canImport(AppKit)
import AppKit
typealias DocumentPlatformImage = NSImage
#endif

enum DocumentFileUtilities {
    static let monospaceExtensions: Set<String> = [
        "csv", "tsv", "json", "jsonl", "xml", "yaml", "yml", "toml",
        "swift", "py", "go", "js", "ts", "tsx", "jsx", "rs", "c", "cpp", "h",
        "java", "kt", "rb", "sh", "zsh", "bash", "sql", "html", "css", "log"
    ]

    static func swiftUIImage(from platformImage: DocumentPlatformImage) -> Image {
        #if canImport(UIKit)
        Image(uiImage: platformImage)
        #elseif canImport(AppKit)
        Image(nsImage: platformImage)
        #endif
    }

    static func copyTextToClipboard(_ text: String) {
        #if canImport(UIKit)
        UIPasteboard.general.string = text
        #elseif canImport(AppKit)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        #endif
    }

    static func copyImageToClipboard(_ image: DocumentPlatformImage) {
        #if canImport(UIKit)
        UIPasteboard.general.image = image
        #elseif canImport(AppKit)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.writeObjects([image])
        #endif
    }

    static func normalizedMediaType(_ raw: String?) -> String {
        guard let raw else { return "" }
        return raw
            .split(separator: ";")
            .first?
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased() ?? ""
    }

    static func isTextMediaType(_ mime: String) -> Bool {
        if mime.hasPrefix("text/") { return true }
        switch mime {
        case "application/json",
             "application/xml",
             "application/javascript",
             "application/typescript",
             "application/yaml",
             "application/x-yaml",
             "application/toml",
             "application/sql",
             "application/x-sh":
            return true
        default:
            return mime.hasSuffix("+json") || mime.hasSuffix("+xml")
        }
    }

    static func isMonospaceMediaType(_ mime: String) -> Bool {
        switch mime {
        case "text/csv",
             "text/tab-separated-values",
             "text/html",
             "text/css",
             "text/javascript",
             "text/xml",
             "application/json",
             "application/xml",
             "application/javascript",
             "application/typescript",
             "application/yaml",
             "application/x-yaml",
             "application/toml",
             "application/sql",
             "application/x-sh":
            return true
        default:
            return mime.hasPrefix("text/x-") || mime.hasSuffix("+json") || mime.hasSuffix("+xml")
        }
    }

    static func fileIconName(for mediaType: String) -> String {
        if mediaType == "application/pdf" { return "doc.richtext.fill" }
        if mediaType.hasPrefix("image/") { return "photo" }
        if mediaType.hasPrefix("audio/") { return "waveform" }
        if mediaType.hasPrefix("video/") { return "film" }
        if mediaType.contains("spreadsheet") || mediaType.contains("excel") || mediaType == "text/csv" {
            return "tablecells"
        }
        if mediaType.contains("presentation") || mediaType.contains("powerpoint") {
            return "rectangle.on.rectangle"
        }
        if mediaType.contains("zip") || mediaType.contains("archive") || mediaType.contains("gzip") {
            return "doc.zipper"
        }
        return "doc.fill"
    }

    static func friendlyTypeLabel(for mime: String) -> String {
        switch mime {
        case "application/pdf": return "PDF Document"
        case "application/zip": return "ZIP Archive"
        case "application/vnd.openxmlformats-officedocument.wordprocessingml.document": return "Word Document"
        case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": return "Spreadsheet"
        case "application/vnd.openxmlformats-officedocument.presentationml.presentation": return "Presentation"
        case "image/svg+xml": return "SVG Image"
        default:
            #if canImport(UniformTypeIdentifiers)
            if let utType = UTType(mimeType: mime), let description = utType.localizedDescription, !description.isEmpty {
                return description
            }
            #endif
            return mime
        }
    }

    static func resolvedFilename(
        payloadFilename: String?,
        itemName: String?,
        itemTitle: String,
        mediaType: String
    ) -> String {
        let rawCandidate = [payloadFilename, itemName, itemTitle]
            .compactMap { $0?.trimmingCharacters(in: .whitespacesAndNewlines) }
            .first(where: { !$0.isEmpty }) ?? "document"

        let lastComponent = (rawCandidate as NSString).lastPathComponent
        let sanitized = sanitizeFilename(lastComponent)
        let base = sanitized.isEmpty ? "document" : sanitized

        let existingExt = (base as NSString).pathExtension
        if !existingExt.isEmpty {
            return base
        }
        if let ext = preferredExtension(for: mediaType), !ext.isEmpty {
            return "\(base).\(ext)"
        }
        return base
    }

    static func sanitizeFilename(_ raw: String) -> String {
        let forbidden = CharacterSet(charactersIn: "/\\:*?\"<>|\r\n\t")
        let cleaned = raw.unicodeScalars.map { scalar -> Character in
            if forbidden.contains(scalar) || scalar.value < 32 {
                return "_"
            }
            return Character(scalar)
        }
        let result = String(cleaned)
            .trimmingCharacters(in: CharacterSet(charactersIn: ". "))
        if result.count > 100 {
            return String(result.prefix(100))
        }
        return result
    }

    static func preferredExtension(for mime: String) -> String? {
        switch mime {
        case "application/pdf": return "pdf"
        case "image/png": return "png"
        case "image/jpeg": return "jpg"
        case "image/webp": return "webp"
        case "image/gif": return "gif"
        case "image/heic": return "heic"
        case "image/svg+xml": return "svg"
        case "text/markdown", "text/x-markdown": return "md"
        case "text/csv": return "csv"
        case "text/plain": return "txt"
        case "text/html": return "html"
        case "application/json": return "json"
        case "application/zip": return "zip"
        case "application/vnd.openxmlformats-officedocument.wordprocessingml.document": return "docx"
        case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": return "xlsx"
        case "application/vnd.openxmlformats-officedocument.presentationml.presentation": return "pptx"
        default:
            #if canImport(UniformTypeIdentifiers)
            return UTType(mimeType: mime)?.preferredFilenameExtension
            #else
            return nil
            #endif
        }
    }
}

final class DocumentTempFileCleanup: @unchecked Sendable {
    private let lock = NSLock()
    private var directories: [URL] = []

    func replace(with newDirectory: URL) {
        let previous: [URL]
        lock.lock()
        previous = directories
        directories = [newDirectory]
        lock.unlock()

        for dir in previous {
            try? FileManager.default.removeItem(at: dir)
        }
    }

    func clearAll() {
        let existing: [URL]
        lock.lock()
        existing = directories
        directories.removeAll()
        lock.unlock()

        for dir in existing {
            try? FileManager.default.removeItem(at: dir)
        }
    }

    deinit {
        clearAll()
    }
}
