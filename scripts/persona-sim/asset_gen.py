#!/usr/bin/env python3
"""Zero-dependency helpers to generate valid synthetic PDF and PNG assets for persona simulations."""

from __future__ import annotations

import struct
import unicodedata
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
    max_lines_per_page = 44
    all_lines = [title, "=" * min(len(title), 72), ""] + lines
    pages: list[list[str]] = [
        all_lines[i : i + max_lines_per_page]
        for i in range(0, max(1, len(all_lines)), max_lines_per_page)
    ]

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


_FONT_5X7: dict[str, tuple[int, int, int, int, int, int, int]] = {
    " ": (0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b00000),
    "!": (0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00000, 0b00100),
    '"': (0b01010, 0b01010, 0b01010, 0b00000, 0b00000, 0b00000, 0b00000),
    "#": (0b01010, 0b01010, 0b11111, 0b01010, 0b11111, 0b01010, 0b01010),
    "$": (0b00100, 0b01111, 0b10100, 0b01110, 0b00101, 0b11110, 0b00100),
    "%": (0b11000, 0b11001, 0b00010, 0b00100, 0b01000, 0b10011, 0b00011),
    "&": (0b01100, 0b10010, 0b10100, 0b01000, 0b10101, 0b10010, 0b01101),
    "'": (0b00100, 0b00100, 0b01000, 0b00000, 0b00000, 0b00000, 0b00000),
    "(": (0b00010, 0b00100, 0b01000, 0b01000, 0b01000, 0b00100, 0b00010),
    ")": (0b01000, 0b00100, 0b00010, 0b00010, 0b00010, 0b00100, 0b01000),
    "*": (0b00000, 0b00100, 0b10101, 0b01110, 0b10101, 0b00100, 0b00000),
    "+": (0b00000, 0b00100, 0b00100, 0b11111, 0b00100, 0b00100, 0b00000),
    ",": (0b00000, 0b00000, 0b00000, 0b00000, 0b00100, 0b00100, 0b01000),
    "-": (0b00000, 0b00000, 0b00000, 0b11111, 0b00000, 0b00000, 0b00000),
    ".": (0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b01100, 0b01100),
    "/": (0b00001, 0b00010, 0b00010, 0b00100, 0b01000, 0b01000, 0b10000),
    "0": (0b01110, 0b10001, 0b10011, 0b10101, 0b11001, 0b10001, 0b01110),
    "1": (0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110),
    "2": (0b01110, 0b10001, 0b00001, 0b00110, 0b01000, 0b10000, 0b11111),
    "3": (0b11110, 0b00001, 0b00001, 0b01110, 0b00001, 0b00001, 0b11110),
    "4": (0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010),
    "5": (0b11111, 0b10000, 0b11110, 0b00001, 0b00001, 0b10001, 0b01110),
    "6": (0b00110, 0b01000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110),
    "7": (0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000),
    "8": (0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110),
    "9": (0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00010, 0b01100),
    ":": (0b00000, 0b01100, 0b01100, 0b00000, 0b01100, 0b01100, 0b00000),
    ";": (0b00000, 0b01100, 0b01100, 0b00000, 0b01100, 0b00100, 0b01000),
    "<": (0b00010, 0b00100, 0b01000, 0b10000, 0b01000, 0b00100, 0b00010),
    "=": (0b00000, 0b00000, 0b11111, 0b00000, 0b11111, 0b00000, 0b00000),
    ">": (0b01000, 0b00100, 0b00010, 0b00001, 0b00010, 0b00100, 0b01000),
    "?": (0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0b00000, 0b00100),
    "@": (0b01110, 0b10001, 0b10111, 0b10101, 0b10110, 0b10000, 0b01110),
    "A": (0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001),
    "B": (0b11110, 0b10001, 0b10001, 0b11110, 0b10001, 0b10001, 0b11110),
    "C": (0b01110, 0b10001, 0b10000, 0b10000, 0b10000, 0b10001, 0b01110),
    "D": (0b11100, 0b10010, 0b10001, 0b10001, 0b10001, 0b10010, 0b11100),
    "E": (0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b11111),
    "F": (0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b10000),
    "G": (0b01110, 0b10001, 0b10000, 0b10111, 0b10001, 0b10001, 0b01111),
    "H": (0b10001, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001),
    "I": (0b01110, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110),
    "J": (0b00111, 0b00010, 0b00010, 0b00010, 0b10010, 0b10010, 0b01100),
    "K": (0b10001, 0b10010, 0b10100, 0b11000, 0b10100, 0b10010, 0b10001),
    "L": (0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b11111),
    "M": (0b10001, 0b11011, 0b10101, 0b10101, 0b10001, 0b10001, 0b10001),
    "N": (0b10001, 0b11001, 0b10101, 0b10011, 0b10001, 0b10001, 0b10001),
    "O": (0b01110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110),
    "P": (0b11110, 0b10001, 0b10001, 0b11110, 0b10000, 0b10000, 0b10000),
    "Q": (0b01110, 0b10001, 0b10001, 0b10001, 0b10101, 0b10010, 0b01101),
    "R": (0b11110, 0b10001, 0b10001, 0b11110, 0b10100, 0b10010, 0b10001),
    "S": (0b01111, 0b10000, 0b10000, 0b01110, 0b00001, 0b00001, 0b11110),
    "T": (0b11111, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100),
    "U": (0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110),
    "V": (0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01010, 0b00100),
    "W": (0b10001, 0b10001, 0b10001, 0b10101, 0b10101, 0b11011, 0b10001),
    "X": (0b10001, 0b10001, 0b01010, 0b00100, 0b01010, 0b10001, 0b10001),
    "Y": (0b10001, 0b10001, 0b01010, 0b00100, 0b00100, 0b00100, 0b00100),
    "Z": (0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b10000, 0b11111),
    "[": (0b01110, 0b01000, 0b01000, 0b01000, 0b01000, 0b01000, 0b01110),
    "\\": (0b10000, 0b01000, 0b01000, 0b00100, 0b00010, 0b00010, 0b00001),
    "]": (0b01110, 0b00010, 0b00010, 0b00010, 0b00010, 0b00010, 0b01110),
    "^": (0b00100, 0b01010, 0b10001, 0b00000, 0b00000, 0b00000, 0b00000),
    "_": (0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b11111),
    "`": (0b01000, 0b00100, 0b00010, 0b00000, 0b00000, 0b00000, 0b00000),
    "a": (0b00000, 0b00000, 0b01110, 0b00001, 0b01111, 0b10001, 0b01111),
    "b": (0b10000, 0b10000, 0b11110, 0b10001, 0b10001, 0b10001, 0b11110),
    "c": (0b00000, 0b00000, 0b01110, 0b10001, 0b10000, 0b10001, 0b01110),
    "d": (0b00001, 0b00001, 0b01111, 0b10001, 0b10001, 0b10001, 0b01111),
    "e": (0b00000, 0b00000, 0b01110, 0b10001, 0b11111, 0b10000, 0b01110),
    "f": (0b00110, 0b01001, 0b01000, 0b11100, 0b01000, 0b01000, 0b01000),
    "g": (0b00000, 0b01111, 0b10001, 0b10001, 0b01111, 0b00001, 0b01110),
    "h": (0b10000, 0b10000, 0b10110, 0b11001, 0b10001, 0b10001, 0b10001),
    "i": (0b00100, 0b00000, 0b01100, 0b00100, 0b00100, 0b00100, 0b01110),
    "j": (0b00010, 0b00000, 0b00110, 0b00010, 0b00010, 0b10010, 0b01100),
    "k": (0b10000, 0b10000, 0b10010, 0b10100, 0b11000, 0b10100, 0b10010),
    "l": (0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110),
    "m": (0b00000, 0b00000, 0b11010, 0b10101, 0b10101, 0b10101, 0b10001),
    "n": (0b00000, 0b00000, 0b11110, 0b10001, 0b10001, 0b10001, 0b10001),
    "o": (0b00000, 0b00000, 0b01110, 0b10001, 0b10001, 0b10001, 0b01110),
    "p": (0b00000, 0b00000, 0b11110, 0b10001, 0b11110, 0b10000, 0b10000),
    "q": (0b00000, 0b00000, 0b01111, 0b10001, 0b01111, 0b00001, 0b00001),
    "r": (0b00000, 0b00000, 0b10110, 0b11001, 0b10000, 0b10000, 0b10000),
    "s": (0b00000, 0b00000, 0b01111, 0b10000, 0b01110, 0b00001, 0b11110),
    "t": (0b01000, 0b01000, 0b11100, 0b01000, 0b01000, 0b01001, 0b00110),
    "u": (0b00000, 0b00000, 0b10001, 0b10001, 0b10001, 0b10011, 0b01101),
    "v": (0b00000, 0b00000, 0b10001, 0b10001, 0b10001, 0b01010, 0b00100),
    "w": (0b00000, 0b00000, 0b10001, 0b10001, 0b10101, 0b10101, 0b01010),
    "x": (0b00000, 0b00000, 0b10001, 0b01010, 0b00100, 0b01010, 0b10001),
    "y": (0b00000, 0b00000, 0b10001, 0b10001, 0b01111, 0b00001, 0b01110),
    "z": (0b00000, 0b00000, 0b11111, 0b00010, 0b00100, 0b01000, 0b11111),
    "{": (0b00110, 0b01000, 0b01000, 0b10000, 0b01000, 0b01000, 0b00110),
    "|": (0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100),
    "}": (0b01100, 0b00010, 0b00010, 0b00001, 0b00010, 0b00010, 0b01100),
    "~": (0b00000, 0b01101, 0b10010, 0b00000, 0b00000, 0b00000, 0b00000),
}

_UNICODE_PUNCT_MAP = str.maketrans({
    "\u2014": "-",
    "\u2013": "-",
    "\u2212": "-",
    "\u2018": "'",
    "\u2019": "'",
    "\u201c": '"',
    "\u201d": '"',
    "\u2026": "...",
    "\u00b0": "deg",
    "\u2192": "->",
    "\u2022": "*",
    "\u00a0": " ",
})


def _sanitize_ascii_label(text: str) -> str:
    """Normalize Unicode punctuation and accents into printable ASCII 0x20..0x7E."""
    translated = str(text or "").translate(_UNICODE_PUNCT_MAP)
    decomposed = unicodedata.normalize("NFKD", translated)
    out: list[str] = []
    for ch in decomposed:
        if unicodedata.combining(ch):
            continue
        if ch == "\r":
            continue
        if ch == "\n":
            out.append("\n")
        elif 0x20 <= ord(ch) <= 0x7E:
            out.append(ch)
        elif ch == "\t":
            out.append(" ")
        else:
            out.append("?")
    return "".join(out)


def _wrap_lines(text: str, max_cols: int) -> list[str]:
    """Wrap text into lines of at most max_cols characters."""
    cols = max(1, int(max_cols))
    lines: list[str] = []
    for para in text.split("\n"):
        stripped = para.strip()
        if not stripped:
            lines.append("")
            continue
        cur = ""
        for word in stripped.split():
            while len(word) > cols:
                if cur:
                    lines.append(cur)
                    cur = ""
                lines.append(word[:cols])
                word = word[cols:]
            if not cur:
                cur = word
            elif len(cur) + 1 + len(word) <= cols:
                cur = f"{cur} {word}"
            else:
                lines.append(cur)
                cur = word
        if cur:
            lines.append(cur)
    return lines or [""]


def decode_png_rgb(raw: bytes) -> tuple[int, int, bytes, dict[str, str]]:
    """Decode an RGB (color type 2, 8-bit) PNG written by write_color_grid_png into (w, h, rgb_bytes, texts)."""
    if not raw.startswith(b"\x89PNG\r\n\x1a\n"):
        raise ValueError("invalid PNG signature")
    pos = 8
    width = height = 0
    idat_parts: list[bytes] = []
    texts: dict[str, str] = {}
    while pos + 8 <= len(raw):
        length = struct.unpack(">I", raw[pos : pos + 4])[0]
        ctype = raw[pos + 4 : pos + 8]
        data = raw[pos + 8 : pos + 8 + length]
        crc_actual = struct.unpack(">I", raw[pos + 8 + length : pos + 12 + length])[0]
        crc_expected = zlib.crc32(ctype + data) & 0xFFFFFFFF
        if crc_actual != crc_expected:
            raise ValueError(f"CRC mismatch in {ctype!r} chunk")
        pos += 12 + length
        if ctype == b"IHDR":
            width, height, bit_depth, color_type, comp, flt, interlace = struct.unpack(">IIBBBBB", data)
            if bit_depth != 8 or color_type != 2 or comp != 0 or flt != 0 or interlace != 0:
                raise ValueError("unsupported PNG format")
        elif ctype == b"tEXt" and b"\x00" in data:
            k, v = data.split(b"\x00", 1)
            texts[k.decode("latin-1")] = v.decode("latin-1")
        elif ctype == b"IDAT":
            idat_parts.append(data)
        elif ctype == b"IEND":
            break
    decompressed = zlib.decompress(b"".join(idat_parts))
    stride = 1 + width * 3
    if len(decompressed) != height * stride:
        raise ValueError(f"unexpected scanline byte length {len(decompressed)} != {height * stride}")
    rgb = bytearray(width * height * 3)
    for y in range(height):
        row_start = y * stride
        if decompressed[row_start] != 0:
            raise ValueError(f"unsupported filter byte {decompressed[row_start]} on row {y}")
        rgb[y * width * 3 : (y + 1) * width * 3] = decompressed[row_start + 1 : row_start + stride]
    return width, height, bytes(rgb), texts


def _set_pixel(buf: bytearray, width: int, height: int, x: int, y: int, rgb: tuple[int, int, int]) -> None:
    if 0 <= x < width and 0 <= y < height:
        idx = (y * width + x) * 3
        buf[idx] = rgb[0]
        buf[idx + 1] = rgb[1]
        buf[idx + 2] = rgb[2]


def _fill_rect(buf: bytearray, width: int, height: int, x0: int, y0: int, x1: int, y1: int, rgb: tuple[int, int, int]) -> None:
    cx0 = max(0, x0)
    cy0 = max(0, y0)
    cx1 = min(width, x1)
    cy1 = min(height, y1)
    r, g, b = rgb
    for y in range(cy0, cy1):
        row_off = y * width * 3
        for x in range(cx0, cx1):
            idx = row_off + x * 3
            buf[idx] = r
            buf[idx + 1] = g
            buf[idx + 2] = b


def write_color_grid_png(
    path: Path,
    width: int = 320,
    height: int = 200,
    palette: list[tuple[int, int, int]] | None = None,
    label_text: str = "",
) -> None:
    """Write a valid RGB PNG image with visible rendered label text and tEXt metadata using pure stdlib."""
    path.parent.mkdir(parents=True, exist_ok=True)
    if width <= 0 or height <= 0:
        raise ValueError(f"width and height must be positive, got {width}x{height}")
    if not palette:
        palette = [(236, 229, 218), (112, 134, 118), (196, 139, 98), (44, 53, 62)]

    bands = len(palette)
    rgb = bytearray(width * height * 3)
    for y in range(height):
        band_y = (y * 2) // height
        row_off = y * width * 3
        for x in range(width):
            idx = ((x * bands) // width + band_y) % bands
            r, g, b = palette[idx]
            if x < 6 or x >= width - 6 or y < 6 or y >= height - 6:
                r, g, b = 36, 40, 46
            p = row_off + x * 3
            rgb[p] = r
            rgb[p + 1] = g
            rgb[p + 2] = b

    clean_label = _sanitize_ascii_label(label_text).strip()
    if clean_label and width >= 24 and height >= 24:
        margin_x = min(12, max(2, width // 10))
        margin_y = min(12, max(2, height // 10))
        pad_x = min(8, max(2, width // 20))
        pad_y = min(6, max(2, height // 20))
        swatch_reserve = max(36, height // 5) if height >= 120 else max(12, height // 4)

        chosen_scale = 1
        chosen_lines: list[str] = []
        chosen_panel_h = 0

        for scale in (2, 1):
            char_adv = 6 * scale
            line_h = 10 * scale
            avail_w = width - 2 * margin_x - 2 * pad_x + scale
            max_cols = max(1, avail_w // char_adv)
            wrapped = _wrap_lines(clean_label, max_cols)
            panel_h = len(wrapped) * line_h - (3 * scale) + 2 * pad_y
            max_panel_for_swatch = height - margin_y - swatch_reserve
            if scale == 2 and panel_h <= max_panel_for_swatch:
                chosen_scale = 2
                chosen_lines = wrapped
                chosen_panel_h = panel_h
                break
            if scale == 1:
                max_panel_h = max(line_h + 2 * pad_y, height - 2 * margin_y)
                max_lines = max(1, (max_panel_h - 2 * pad_y + 3) // line_h)
                if len(wrapped) > max_lines:
                    clipped = wrapped[:max_lines]
                    if max_cols >= 4:
                        clipped[-1] = clipped[-1][: max_cols - 3].rstrip() + "..."
                    else:
                        clipped[-1] = "." * max_cols
                    wrapped = clipped
                    panel_h = len(wrapped) * line_h - (3 * scale) + 2 * pad_y
                chosen_scale = 1
                chosen_lines = wrapped
                chosen_panel_h = min(panel_h, max_panel_h)

        x0 = margin_x
        x1 = width - margin_x
        y0 = max(margin_y, height - margin_y - chosen_panel_h)
        y1 = height - margin_y

        if x1 > x0 + 2 and y1 > y0 + 2:
            _fill_rect(rgb, width, height, x0, y0, x1, y1, (36, 40, 46))
            _fill_rect(rgb, width, height, x0 + 1, y0 + 1, x1 - 1, y1 - 1, (248, 248, 246))

            fg = (18, 22, 26)
            char_adv = 6 * chosen_scale
            line_h = 10 * chosen_scale
            text_x0 = x0 + pad_x
            text_y0 = y0 + pad_y

            for line_idx, line in enumerate(chosen_lines):
                cy = text_y0 + line_idx * line_h
                if cy + 7 * chosen_scale > y1 - 1:
                    break
                for col_idx, ch in enumerate(line):
                    cx = text_x0 + col_idx * char_adv
                    if cx + 5 * chosen_scale > x1 - 1:
                        break
                    glyph = _FONT_5X7.get(ch, _FONT_5X7["?"])
                    for gy, row_bits in enumerate(glyph):
                        for gx in range(5):
                            if row_bits & (1 << (4 - gx)):
                                _fill_rect(
                                    rgb,
                                    width,
                                    height,
                                    cx + gx * chosen_scale,
                                    cy + gy * chosen_scale,
                                    cx + (gx + 1) * chosen_scale,
                                    cy + (gy + 1) * chosen_scale,
                                    fg,
                                )

    rows = bytearray()
    row_bytes = width * 3
    for y in range(height):
        rows.append(0)
        rows.extend(rgb[y * row_bytes : (y + 1) * row_bytes])

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
