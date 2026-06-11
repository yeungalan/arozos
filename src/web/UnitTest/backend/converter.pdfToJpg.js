/*
    PDF to JPG Conversion Test

    This script renders a page of a PDF document into a JPEG.
    A host PDF rasterizer (pdftoppm / pdftocairo / mutool / ghostscript) is used
    when available for faithful rendering; otherwise the largest embedded image
    in the PDF is extracted (works for scanned / image-based PDFs).

    To test this, put a PDF named test.pdf on your desktop.
    Signature: converter.pdfToJpg(src, dest, page, dpi)
*/

console.log("PDF to JPG Conversion Test");
var srcPath = "user:/Desktop/test.pdf";
var destPath = "user:/Desktop/test_pdf.jpg";

requirelib("filelib");
if (!filelib.fileExists(srcPath)) {
    sendResp("File not exists! Put a PDF at " + srcPath + " to run this test.");
} else {
    var loaded = requirelib("converter");
    if (loaded) {
        if (!converter.pdfEngineAvailable()) {
            console.log("No host PDF rasterizer found - using embedded image extraction fallback");
        }
        // Render the first page at 150 DPI
        if (converter.pdfToJpg(srcPath, destPath, 1, 150)) {
            sendResp("OK");
        } else {
            sendResp("Failed to convert PDF to JPG");
        }
    } else {
        console.log("Failed to load lib: converter");
    }
}
