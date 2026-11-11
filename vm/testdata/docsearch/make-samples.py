#!/usr/bin/env python3
# Writes the sample documents of the document search tests (vm/e2e.d/97-search.sh).
# Everything here is generated, CC0. Run: python3 make-samples.py <out dir>
# (Pillow and a TrueType font are only needed for the OCR picture).
import os, sys, zipfile

out = sys.argv[1] if len(sys.argv) > 1 else "."
os.makedirs(out, exist_ok=True)

def write(name, data, mode="w"):
    with open(os.path.join(out, name), mode) as f:
        f.write(data)

# PDF 1.4 with one page of text (Helvetica), xref offsets computed.
def pdf(lines):
    content = "BT /F1 14 Tf 72 720 Td 18 TL " + " ".join("(%s) '" % l for l in lines) + " ET"
    objs = [
        "<< /Type /Catalog /Pages 2 0 R >>",
        "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
        "<< /Length %d >>\nstream\n%s\nendstream" % (len(content), content),
        "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    b = "%PDF-1.4\n"
    offs = []
    for i, o in enumerate(objs):
        offs.append(len(b))
        b += "%d 0 obj\n%s\nendobj\n" % (i + 1, o)
    x = len(b)
    b += "xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)
    b += "".join("%010d 00000 n \n" % o for o in offs)
    b += "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, x)
    return b.encode("latin-1")

write("invoice-2024.pdf", pdf(["Invoice number 4711", "Customer: Marmalade Gardens Ltd", "Total due: 1250 EUR"]), "wb")

def zipdoc(name, files):
    with zipfile.ZipFile(os.path.join(out, name), "w", zipfile.ZIP_DEFLATED) as z:
        for n, d in files.items():
            z.writestr(n, d)

W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"'
zipdoc("report.docx", {
    "[Content_Types].xml": '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>',
    "word/document.xml": '<?xml version="1.0"?><w:document %s><w:body><w:p><w:r><w:t>Quarterly report about the lighthouse renovation</w:t></w:r></w:p><w:p><w:r><w:t xml:space="preserve">The budget for </w:t></w:r><w:r><w:t>scaffolding</w:t></w:r><w:r><w:t xml:space="preserve"> was approved.</w:t></w:r></w:p></w:body></w:document>' % W,
})
zipdoc("budget.xlsx", {
    "xl/workbook.xml": '<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets><sheet name="Expenses" sheetId="1"/></sheets></workbook>',
    "xl/sharedStrings.xml": '<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Pineapple imports</t></si><si><t>Freight</t></si></sst>',
    "xl/worksheets/sheet1.xml": '<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><v>420</v></c></row></sheetData></worksheet>',
})
A = 'xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"'
zipdoc("slides.pptx", {
    "ppt/slides/slide1.xml": '<?xml version="1.0"?><p:sld %s><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Roadmap for the telescope project</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>' % A,
})
zipdoc("notes.odt", {
    "mimetype": "application/vnd.oasis.opendocument.text",
    "content.xml": '<?xml version="1.0"?><office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"><office:body><office:text><text:p>Recette de la tarte aux myrtilles</text:p><text:p>Préchauffer le four à 180 degrés.</text:p></office:text></office:body></office:document-content>',
})
write("meeting.eml", "From: Alice <alice@example.org>\r\nTo: Bob <bob@example.org>\r\nSubject: =?utf-8?q?R=C3=A9union_du_jeudi?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nWe meet near the windmill at noon. Bring the hazelnut cake.\r\n")
write("README.md", "# Garden robot\n\nThe robot waters the tomatoes every morning.\n")
write("server.py", "def zeppelin_launch(altitude):\n    return altitude * 2\n")

try:
    from PIL import Image, ImageDraw, ImageFont
    img = Image.new("L", (900, 220), 255)
    d = ImageDraw.Draw(img)
    font = ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", 44)
    d.text((30, 40), "Warranty card", font=font, fill=0)
    d.text((30, 120), "Serial KANGAROO 2291", font=font, fill=0)
    img.save(os.path.join(out, "scan.png"))
except Exception as e:  # no Pillow or font: the OCR picture is committed
    print("scan.png not written:", e)
