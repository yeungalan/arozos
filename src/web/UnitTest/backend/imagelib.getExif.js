console.log("Image EXIF Reading Test");
//To test this, put a test.jpg with EXIF metadata (e.g. a photo straight
//from a camera or phone) on your desktop
var imagePath = "user:/Desktop/test.jpg";
//Check if the file exists
requirelib("filelib");
if (!filelib.fileExists(imagePath)){
	sendResp("File not exists!")
}else{
    //Require the image library
    var loaded = requirelib("imagelib");
    if (loaded) {
        //Only decode EXIF when the image actually carries it
        if (imagelib.hasExif(imagePath)) {
            var exif = JSON.parse(imagelib.getExif(imagePath));
            sendJSONResp(JSON.stringify(exif));
        } else {
            sendJSONResp(JSON.stringify({ error: "No EXIF metadata found in image" }));
        }
    } else {
        console.log("Failed to load lib: imagelib");
    }
}
