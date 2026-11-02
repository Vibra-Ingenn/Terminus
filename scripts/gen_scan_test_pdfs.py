"""Build colour, grey, 1-bit, and 3-page scan PDFs for Terminus tests."""
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

OUT = Path(__file__).resolve().parents[1] / "internal" / "tools" / "testdata" / "scans"
OUT.mkdir(parents=True, exist_ok=True)


def page(label: str, mode: str) -> Image.Image:
    if mode == "1":
        img = Image.new("1", (400, 120), 1)
        d = ImageDraw.Draw(img)
        d.text((12, 48), label, fill=0)
        return img
    if mode == "L":
        img = Image.new("L", (400, 120), 240)
        d = ImageDraw.Draw(img)
        d.text((12, 48), label, fill=0)
        return img
    img = Image.new("RGB", (400, 120), (255, 255, 255))
    d = ImageDraw.Draw(img)
    d.text((12, 48), label, fill=(0, 0, 0))
    return img


def save_pdf(images: list[Image.Image], name: str) -> None:
    path = OUT / name
    rgb = [im.convert("RGB") if im.mode != "RGB" else im for im in images]
    rgb[0].save(path, save_all=True, append_images=rgb[1:], resolution=150.0)


def main() -> None:
    save_pdf([page("COLOUR-SCAN-TEST", "RGB")], "scan_colour.pdf")
    save_pdf([page("GREY-SCAN-TEST", "L")], "scan_grey.pdf")
    save_pdf([page("BIT1-SCAN-TEST", "1")], "scan_1bit.pdf")
    save_pdf(
        [
            page("COLOUR-SCAN-TEST", "RGB"),
            page("GREY-SCAN-TEST", "L"),
            page("BIT1-SCAN-TEST", "1"),
        ],
        "scan_three_pages.pdf",
    )
    g4 = OUT / "scan_ccitt.tiff"
    page("CCITT-SCAN-TEST", "1").save(g4, compression="group4")
    print("wrote", OUT)


if __name__ == "__main__":
    main()
