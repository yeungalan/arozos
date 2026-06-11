/*
    Converter Helpers Test

    Demonstrates the converter type-detection helper. This is a pure string
    check and does not require any file to exist on disk.
*/

console.log("Converter Helper Functions Test");
if (requirelib("converter")) {
    var results = {
        "test.arw is raw": converter.isRawFile("user:/Desktop/test.arw"),
        "test.CR2 is raw": converter.isRawFile("user:/Desktop/test.CR2"),
        "test.jpg is raw": converter.isRawFile("user:/Desktop/test.jpg"),
        "supportedRawFormats": converter.supportedRawFormats()
    };
    sendJSONResp(JSON.stringify(results));
} else {
    console.log("Failed to load lib: converter");
    sendResp("Converter lib not found");
}
