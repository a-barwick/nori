import Foundation
import CoreGraphics
import ImageIO
import UniformTypeIdentifiers

public enum DetectedImageFormat: String, Sendable, Equatable {
    case png
    case jpeg
    case webp
    case heic
    case other
    case unknown
}

public struct NormalizedImageUpload: Sendable, Equatable {
    public let data: Data
    public let filename: String
    public let mediaType: String
    public let wasTranscoded: Bool

    public init(data: Data, filename: String, mediaType: String, wasTranscoded: Bool) {
        self.data = data
        self.filename = filename
        self.mediaType = mediaType
        self.wasTranscoded = wasTranscoded
    }
}

public enum ImageUploadNormalizationError: Error, LocalizedError, Sendable, Equatable {
    case emptyData
    case unsupportedImageFormat
    case encodingFailed
    case exceedsMaxBytes(limit: Int)

    public var errorDescription: String? {
        switch self {
        case .emptyData:
            return "The selected photo is empty."
        case .unsupportedImageFormat:
            return "This photo format could not be read. Try a PNG, JPEG, WebP, or HEIC image."
        case .encodingFailed:
            return "Could not prepare this photo for upload."
        case .exceedsMaxBytes(let limit):
            let mb = max(1, limit / (1024 * 1024))
            return "This photo is still larger than the \(mb) MB upload limit."
        }
    }
}

/// Hardware-accelerated on-device photo normalizer using ImageIO/CoreGraphics.
///
/// - Preserves small PNG screenshots (≤ `maxBytes`) as real `image/png` with `.png` extension.
/// - Preserves small JPEG and WebP images (≤ `maxBytes`) with matching media types and extensions.
/// - Transcodes HEIC/HEIF, other decodable image formats, and any oversized image (> `maxBytes`)
///   to orientation-baked JPEG with `image/jpeg` + `.jpg`, downscaling/compressing to fit cleanly
///   under `maxBytes`.
public enum ImageUploadNormalizer: Sendable {
    /// Matches backend `state.MaxUploadBytes` (`10 << 20` = 10 MiB).
    public static let defaultMaxBytes: Int = 10 * 1024 * 1024

    private static let maxInitialPixelDimension: Int = 4096
    private static let minPixelDimension: Int = 256
    private static let jpegQualities: [CGFloat] = [0.86, 0.76, 0.64, 0.50]
    private static let scaleFactors: [CGFloat] = [1.0, 0.75, 0.55, 0.40, 0.28, 0.20, 0.14]

    public static func detectFormat(of data: Data) -> DetectedImageFormat {
        guard !data.isEmpty else { return .unknown }

        if hasPNGMagicBytes(data) {
            return .png
        }
        if hasJPEGMagicBytes(data) {
            return .jpeg
        }
        if hasWebPMagicBytes(data) {
            return .webp
        }
        if hasHEIFMagicBytes(data) {
            return .heic
        }

        if let source = CGImageSourceCreateWithData(data as CFData, nil),
           CGImageSourceGetCount(source) > 0 {
            if let uti = CGImageSourceGetType(source) as String? {
                let lower = uti.lowercased()
                if lower == UTType.png.identifier { return .png }
                if lower == UTType.jpeg.identifier { return .jpeg }
                if lower == "org.webmproject.webp" || lower.contains("webp") { return .webp }
                if lower == UTType.heic.identifier || lower == UTType.heif.identifier || lower.contains("heic") || lower.contains("heif") {
                    return .heic
                }
            }
            return .other
        }

        return .unknown
    }

    public static func normalize(
        data: Data,
        suggestedFilename: String,
        maxBytes: Int = defaultMaxBytes
    ) throws -> NormalizedImageUpload {
        guard !data.isEmpty else {
            throw ImageUploadNormalizationError.emptyData
        }

        let effectiveMaxBytes = max(1024, maxBytes)
        let format = detectFormat(of: data)

        // Preserve already-supported formats without re-encoding when within size limits.
        if data.count <= effectiveMaxBytes {
            switch format {
            case .png:
                return NormalizedImageUpload(
                    data: data,
                    filename: replacingExtension(suggestedFilename, with: "png"),
                    mediaType: "image/png",
                    wasTranscoded: false
                )
            case .jpeg:
                return NormalizedImageUpload(
                    data: data,
                    filename: replacingExtension(suggestedFilename, with: "jpg"),
                    mediaType: "image/jpeg",
                    wasTranscoded: false
                )
            case .webp:
                return NormalizedImageUpload(
                    data: data,
                    filename: replacingExtension(suggestedFilename, with: "webp"),
                    mediaType: "image/webp",
                    wasTranscoded: false
                )
            case .heic, .other, .unknown:
                break
            }
        }

        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
              CGImageSourceGetCount(source) > 0 else {
            throw ImageUploadNormalizationError.unsupportedImageFormat
        }

        let sourceMaxDimension = imageMaxDimension(for: source) ?? maxInitialPixelDimension
        let baseMaxDimension = max(minPixelDimension, min(sourceMaxDimension, maxInitialPixelDimension))
        let jpgFilename = replacingExtension(suggestedFilename, with: "jpg")

        var producedAnyJPEG = false
        for factor in scaleFactors {
            let targetDimension = max(minPixelDimension, Int((CGFloat(baseMaxDimension) * factor).rounded()))
            guard let cgImage = createOrientedImage(from: source, maxPixelSize: targetDimension) else {
                continue
            }
            let opaqueImage = flattenAlphaIfNeeded(cgImage)

            for quality in jpegQualities {
                if let jpegData = encodeJPEG(opaqueImage, quality: quality), hasJPEGMagicBytes(jpegData) {
                    producedAnyJPEG = true
                    if jpegData.count <= effectiveMaxBytes {
                        return NormalizedImageUpload(
                            data: jpegData,
                            filename: jpgFilename,
                            mediaType: "image/jpeg",
                            wasTranscoded: true
                        )
                    }
                }
            }

            if targetDimension == minPixelDimension {
                break
            }
        }

        throw producedAnyJPEG
            ? ImageUploadNormalizationError.exceedsMaxBytes(limit: effectiveMaxBytes)
            : ImageUploadNormalizationError.encodingFailed
    }

    public static func replacingExtension(_ filename: String, with ext: String) -> String {
        let trimmed = filename.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanExt = ext.trimmingCharacters(in: CharacterSet(charactersIn: ".")).lowercased()
        guard !trimmed.isEmpty else {
            return "photo.\(cleanExt)"
        }
        let ns = trimmed as NSString
        let base = ns.deletingPathExtension
        let stem = base.isEmpty ? trimmed : base
        return "\(stem).\(cleanExt)"
    }

    // MARK: - Private Helpers

    private static func hasPNGMagicBytes(_ data: Data) -> Bool {
        let sig: [UInt8] = [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]
        guard data.count >= sig.count else { return false }
        return data.prefix(sig.count).elementsEqual(sig)
    }

    private static func hasJPEGMagicBytes(_ data: Data) -> Bool {
        let sig: [UInt8] = [0xFF, 0xD8, 0xFF]
        guard data.count >= sig.count else { return false }
        return data.prefix(sig.count).elementsEqual(sig)
    }

    private static func hasWebPMagicBytes(_ data: Data) -> Bool {
        guard data.count >= 12 else { return false }
        let riff: [UInt8] = [0x52, 0x49, 0x46, 0x46] // "RIFF"
        let webp: [UInt8] = [0x57, 0x45, 0x42, 0x50] // "WEBP"
        return data.prefix(4).elementsEqual(riff) && data.dropFirst(8).prefix(4).elementsEqual(webp)
    }

    private static func hasHEIFMagicBytes(_ data: Data) -> Bool {
        guard data.count >= 12 else { return false }
        let ftyp: [UInt8] = [0x66, 0x74, 0x79, 0x70] // "ftyp"
        guard data.dropFirst(4).prefix(4).elementsEqual(ftyp) else { return false }
        guard let brand = String(data: data.dropFirst(8).prefix(4), encoding: .ascii)?.lowercased() else {
            return false
        }
        let heifBrands: Set<String> = ["heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "heif"]
        return heifBrands.contains(brand)
    }

    private static func imageMaxDimension(for source: CGImageSource) -> Int? {
        guard let props = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any] else {
            return nil
        }
        let width = (props[kCGImagePropertyPixelWidth] as? NSNumber)?.intValue ?? 0
        let height = (props[kCGImagePropertyPixelHeight] as? NSNumber)?.intValue ?? 0
        let maxDim = max(width, height)
        return maxDim > 0 ? maxDim : nil
    }

    private static func createOrientedImage(from source: CGImageSource, maxPixelSize: Int) -> CGImage? {
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: maxPixelSize
        ]
        if let thumb = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) {
            return thumb
        }
        return CGImageSourceCreateImageAtIndex(source, 0, nil)
    }

    private static func flattenAlphaIfNeeded(_ image: CGImage) -> CGImage {
        let alphaInfo = image.alphaInfo
        let hasAlpha: Bool
        switch alphaInfo {
        case .none, .noneSkipFirst, .noneSkipLast:
            hasAlpha = false
        default:
            hasAlpha = true
        }
        guard hasAlpha else { return image }

        let width = image.width
        let height = image.height
        guard width > 0, height > 0 else { return image }

        let colorSpace = image.colorSpace?.model == .rgb
            ? (image.colorSpace ?? CGColorSpaceCreateDeviceRGB())
            : CGColorSpaceCreateDeviceRGB()

        guard let context = CGContext(
            data: nil,
            width: width,
            height: height,
            bitsPerComponent: 8,
            bytesPerRow: 0,
            space: colorSpace,
            bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue
        ) else {
            return image
        }

        context.setFillColor(red: 1.0, green: 1.0, blue: 1.0, alpha: 1.0)
        context.fill(CGRect(x: 0, y: 0, width: width, height: height))
        context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
        return context.makeImage() ?? image
    }

    private static func encodeJPEG(_ image: CGImage, quality: CGFloat) -> Data? {
        let destinationData = NSMutableData()
        guard let destination = CGImageDestinationCreateWithData(
            destinationData as CFMutableData,
            UTType.jpeg.identifier as CFString,
            1,
            nil
        ) else {
            return nil
        }
        let options: [CFString: Any] = [
            kCGImageDestinationLossyCompressionQuality: quality
        ]
        CGImageDestinationAddImage(destination, image, options as CFDictionary)
        guard CGImageDestinationFinalize(destination) else {
            return nil
        }
        return destinationData as Data
    }
}
