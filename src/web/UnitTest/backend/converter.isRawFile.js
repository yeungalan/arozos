/*
    Converter Helpers Test

    Demonstrates the converter type-detection helpers. These are pure string
    checks and do not require any file to exist on disk.
*/

console.log("Converter Helper Functions Test");
if (requirelib("converter")) {
    var results = {
        "test.arw is raw": converter.isRawFile("user:/Desktop/test.arw"),
        "test.CR2 is raw": converter.isRawFile("user:/Desktop/test.CR2"),
        "test.jpg is raw": converter.isRawFile("user:/Desktop/test.jpg"),
        "test.pdf is pdf": converter.isPdfFile("user:/Desktop/test.pdf"),
        "test.jpg is pdf": converter.isPdfFile("user:/Desktop/test.jpg"),
        "supportedRawFormats": converter.supportedRawFormats(),
        "pdfEngineAvailable": converter.pdfEngineAvailable()
    };
    sendJSONResp(JSON.stringify(results));
} else {
    console.log("Failed to load lib: converter");
    sendResp("Converter lib not found");
}
