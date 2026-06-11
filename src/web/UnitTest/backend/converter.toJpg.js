/*
    Auto-detecting Conversion Test

    converter.toJpg(src, dest) inspects the source file extension and routes the
    request to the RAW or PDF converter automatically.

    To test this, put either a RAW photo or a PDF at the source path below.
*/

console.log("Converter Auto-detect Test");
var srcPath = "user:/Desktop/test.pdf"; // try also user:/Desktop/test.arw
var destPath = "user:/Desktop/test_auto.jpg";

requirelib("filelib");
if (!filelib.fileExists(srcPath)) {
    sendResp("File not exists! Put a RAW or PDF file at " + srcPath + " to run this test.");
} else {
    if (requirelib("converter")) {
        if (converter.toJpg(srcPath, destPath)) {
            sendResp("OK");
        } else {
            sendResp("Failed to auto-convert " + srcPath + " to JPG");
        }
    } else {
        console.log("Failed to load lib: converter");
    }
}
