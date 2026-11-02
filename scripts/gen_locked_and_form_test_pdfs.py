"""Build locked PDFs and a text-in-a-form PDF for Terminus tests.

Run: uvx --with pypdf --with cryptography --with reportlab --with pillow python -I scripts/gen_locked_and_form_test_pdfs.py

- form_text.pdf: all the page text sits inside a Form XObject.
- locked_rc4_40_scan.pdf: a JPEG scan, locked with RC4 40-bit, no password needed to open.
- locked_aes128.pdf / locked_aes256.pdf: text PDFs, AES, no password needed to open.
- locked_password.pdf: needs the password "secret" to open.
"""
from io import BytesIO
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont
from pypdf import PdfReader, PdfWriter
from reportlab.lib.pagesizes import letter
from reportlab.pdfgen import canvas

OUT = Path(__file__).resolve().parents[1] / "internal" / "tools" / "testdata" / "locked"
OUT.mkdir(parents=True, exist_ok=True)


def text_pdf(lines: list[str], in_form: bool = False) -> bytes:
    buf = BytesIO()
    c = canvas.Canvas(buf, pagesize=letter)
    if in_form:
        c.beginForm("body")
    y = 700
    for line in lines:
        c.setFont("Helvetica", 14)
        c.drawString(72, y, line)
        y -= 24
    if in_form:
        c.endForm()
        c.doForm("body")
    c.showPage()
    c.save()
    return buf.getvalue()


def scan_pdf(lines: list[str]) -> bytes:
    img = Image.new("RGB", (1275, 600), "white")
    d = ImageDraw.Draw(img)
    try:
        font = ImageFont.truetype("arial.ttf", 48)
    except OSError:
        font = ImageFont.load_default()
    y = 60
    for line in lines:
        d.text((60, y), line, fill="black", font=font)
        y += 90
    buf = BytesIO()
    img.save(buf, "PDF", resolution=150.0)  # Pillow stores the page as a JPEG (DCTDecode)
    return buf.getvalue()


def lock(data: bytes, name: str, user: str, algorithm: str) -> None:
    w = PdfWriter(clone_from=PdfReader(BytesIO(data)))
    w.encrypt(user_password=user, owner_password="owner-pass", algorithm=algorithm)
    with open(OUT / name, "wb") as f:
        w.write(f)


body = ["Locked statement test", "Account balance 1234.56", "Thank you"]
(OUT / "form_text.pdf").write_bytes(text_pdf(["Text inside a form", "Second line of the form"], in_form=True))
lock(scan_pdf(["LOCKED SCAN TEST", "TOTAL 98.76"]), "locked_rc4_40_scan.pdf", "", "RC4-40")
lock(text_pdf(body), "locked_aes128.pdf", "", "AES-128")
lock(text_pdf(body), "locked_aes256.pdf", "", "AES-256")
lock(text_pdf(body), "locked_password.pdf", "secret", "AES-128")
print("wrote", sorted(p.name for p in OUT.iterdir()))
