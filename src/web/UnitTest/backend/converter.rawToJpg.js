/*
    RAW to JPG Conversion Test

    This script converts a camera RAW photo (ARW / CR2 / DNG / NEF / RAF / ORF)
    into a JPEG by extracting its embedded full-resolution preview.

    To test this, put a RAW photo named test.arw on your desktop.
*/

console.log("RAW to JPG Conversion Test");
var srcPath = "user:/Desktop/test.arw";
var destPath = "user:/Desktop/test_raw.jpg";

requirelib("filelib");
if (!filelib.fileExists(srcPath)) {
    sendResp("File not exists! Put a RAW photo at " + srcPath + " to run this test.");
} else {
    var loaded = requirelib("converter");
    if (loaded) {
        if (converter.rawToJpg(srcPath, destPath)) {
            sendResp("OK");
        } else {
            sendResp("Failed to convert RAW to JPG");
        }
    } else {
        console.log("Failed to load lib: converter");
    }
}
