/*
    Image EXIF Reading Function

    This script demonstrates how to read EXIF metadata from an image using
    imagelib. It checks the desktop test.jpg for EXIF metadata and returns it
    as a JSON object.

    imagelib.getExif() returns a JSON string keyed by EXIF field name, where
    each value is itself a JSON fragment (strings stay quoted, rationals look
    like "45/10"), so parse the value again to use it on the front-end.
*/

if (requirelib("imagelib")){
    var imagePath = "user:/Desktop/test.jpg";
    if (imagelib.hasExif(imagePath)){
        //Image carries EXIF metadata, decode and return it
        var exif = JSON.parse(imagelib.getExif(imagePath));
        sendJSONResp(JSON.stringify(exif));
    }else{
        //No EXIF metadata present (e.g. a screenshot or stripped image)
        sendJSONResp(JSON.stringify({
            error: "No EXIF metadata found in image"
        }));
    }
}else{
    console.log("Image lib not found");
}
