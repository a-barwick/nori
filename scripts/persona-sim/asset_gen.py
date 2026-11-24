#!/usr/bin/env python3
"""Zero-dependency helpers to generate valid synthetic PDF and PNG assets for persona simulations."""

from __future__ import annotations

import struct
import zlib
from pathlib import Path


def _pdf_escape(text: str) -> str:
    return (
        text.replace("\\", "\\\\")
        .replace("(", "\\(")
        .replace(")", "\\)")
    )


def write_text_pdf(path: Path, title: str, lines: list[str]) -> None:
    """Write a valid single- or multi-page PDF 1.4 containing extractable text lines."""
    path.parent.mkdir(parents=True, exist_ok=True)
    # Chunk lines into pages of ~44 lines each
    max_lines_per_page = 44
    all_lines = [title, "=" * min(len(title), 72), ""] + lines
    pages: list[list[str]] = [
        all_lines[i : i + max_lines_per_page]
        for i in range(0, max(1, len(all_lines)), max_lines_per_page)
    ]

    # Object layout:
    # 1: Catalog
    # 2: Pages
    # 3: Font (Helvetica)
    # For each page p (0..N-1):
    #   4 + 2*p: Page object
    #   5 + 2*p: Content stream
    objects: list[bytes] = []

    page_ids = [4 + 2 * i for i in range(len(pages))]
    kids = " ".join(f"{pid} 0 R" for pid in page_ids)
    objects.append(b"<< /Type /Catalog /Pages 2 0 R >>")
    objects.append(f"<< /Type /Pages /Kids [{kids}] /Count {len(pages)} >>".encode("ascii"))
    objects.append(b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

    for idx, page_lines in enumerate(pages):
        content_id = 5 + 2 * idx
        objects.append(
            f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "
            f"/Resources << /Font << /F1 3 0 R >> >> /Contents {content_id} 0 R >>".encode("ascii")
        )
        stream_parts = ["BT", "/F1 10 Tf", "50 750 Td", "14 TL"]
        for line_idx, raw_line in enumerate(page_lines):
            # Keep ASCII-safe for Type1 Helvetica while preserving readability
            safe_line = raw_line.encode("latin-1", errors="replace").decode("latin-1")
            escaped = _pdf_escape(safe_line)
            if line_idx == 0:
                stream_parts.append(f"({escaped}) Tj")
            else:
                stream_parts.append(f"T* ({escaped}) Tj")
        stream_parts.append("ET")
        stream_bytes = ("\n".join(stream_parts) + "\n").encode("latin-1")
        obj = (
            f"<< /Length {len(stream_bytes)} >>\nstream\n".encode("ascii")
            + stream_bytes
            + b"endstream"
        )
        objects.append(obj)

    buf = bytearray(b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
    offsets: list[int] = [0]
    for obj_num, body in enumerate(objects, start=1):
        offsets.append(len(buf))
        buf.extend(f"{obj_num} 0 obj\n".encode("ascii"))
        buf.extend(body)
        buf.extend(b"\nendobj\n")

    xref_pos = len(buf)
    buf.extend(f"xref\n0 {len(objects) + 1}\n".encode("ascii"))
    buf.extend(b"0000000000 65535 f \n")
    for off in offsets[1:]:
        buf.extend(f"{off:010d} 00000 n \n".encode("ascii"))
    buf.extend(
        f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{xref_pos}\n%%EOF\n".encode(
            "ascii"
        )
    )
    path.write_bytes(bytes(buf))


def _png_chunk(chunk_type: bytes, data: bytes) -> bytes:
    crc = zlib.crc32(chunk_type + data) & 0xFFFFFFFF
    return struct.pack(">I", len(data)) + chunk_type + data + struct.pack(">I", crc)


def write_color_grid_png(
    path: Path,
    width: int = 320,
    height: int = 200,
    palette: list[tuple[int, int, int]] | None = None,
    label_text: str = "",
) -> None:
    """Write a valid RGB PNG image (with optional tEXt metadata chunk) using pure stdlib."""
    path.parent.mkdir(parents=True, exist_ok=True)
    if not palette:
        palette = [(236, 229, 218), (112, 134, 118), (196, 139, 98), (44, 53, 62)]

    bands = len(palette)
    rows = bytearray()
    for y in range(height):
        rows.append(0)  # Filter type 0 (None)
        band_y = (y * 2) // height
        for x in range(width):
            idx = ((x * bands) // width + band_y) % bands
            r, g, b = palette[idx]
            # Subtle border frame
            if x < 6 or x >= width - 6 or y < 6 or y >= height - 6:
                r, g, b = 36, 40, 46
            rows.extend((r, g, b))

    ihdr = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    idat = zlib.compress(bytes(rows), level=6)

    out = bytearray(b"\x89PNG\r\n\x1a\n")
    out.extend(_png_chunk(b"IHDR", ihdr))
    if label_text:
        text_data = b"Description\x00" + label_text.encode("latin-1", errors="replace")
        out.extend(_png_chunk(b"tEXt", text_data))
    out.extend(_png_chunk(b"IDAT", idat))
    out.extend(_png_chunk(b"IEND", b""))
    path.write_bytes(bytes(out))


if __name__ == "__main__":
    print("asset_gen ready")
