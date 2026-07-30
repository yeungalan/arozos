/*
    Inventory - local product catalogue and barcode helpers

    A barcode-to-name catalogue kept entirely on this server. There are no
    outbound lookups: nothing about what you scan ever leaves the machine.

    It fills up two ways:
      - every item you name teaches it, so the second warehouse to stock the
        same product gets the name for free
      - you can import a UPC/JAN list you already have (CSV or JSON)

    Also holds the offline barcode arithmetic worth having on a handheld: an
    EAN-13/UPC-A check digit test that catches a misread label before it becomes
    a bogus item, UPC-A to EAN-13 normalisation so a 12-digit scan matches a
    13-digit catalogue row, and the GS1 prefix table that names the issuing
    country - which is what tells you a 49x code is a Japanese JAN.
*/

var CAT_PATH = INV_DIR + "/catalogue.json";
var CAT_CAP = 200000;   // generous, but stops a bad import eating the disk

/*
    GS1 prefix ranges, low-high inclusive on the first three digits. Only the
    ranges worth naming to an operator staring at an unknown code.
*/
var CAT_GS1_PREFIXES = [
    { from: 0, to: 19, name: "UPC (USA / Canada)" },
    { from: 20, to: 29, name: "In-store code" },
    { from: 30, to: 39, name: "UPC (USA drugs)" },
    { from: 60, to: 139, name: "UPC (USA / Canada)" },
    { from: 200, to: 299, name: "In-store code" },
    { from: 300, to: 379, name: "France" },
    { from: 380, to: 380, name: "Bulgaria" },
    { from: 383, to: 383, name: "Slovenia" },
    { from: 385, to: 385, name: "Croatia" },
    { from: 387, to: 387, name: "Bosnia and Herzegovina" },
    { from: 400, to: 440, name: "Germany" },
    { from: 450, to: 459, name: "JAN (Japan)" },
    { from: 460, to: 469, name: "Russia" },
    { from: 471, to: 471, name: "Taiwan" },
    { from: 474, to: 474, name: "Estonia" },
    { from: 475, to: 475, name: "Latvia" },
    { from: 477, to: 477, name: "Lithuania" },
    { from: 479, to: 479, name: "Sri Lanka" },
    { from: 480, to: 480, name: "Philippines" },
    { from: 482, to: 482, name: "Ukraine" },
    { from: 484, to: 484, name: "Moldova" },
    { from: 485, to: 485, name: "Armenia" },
    { from: 486, to: 486, name: "Georgia" },
    { from: 487, to: 487, name: "Kazakhstan" },
    { from: 489, to: 489, name: "Hong Kong" },
    { from: 490, to: 499, name: "JAN (Japan)" },
    { from: 500, to: 509, name: "United Kingdom" },
    { from: 520, to: 521, name: "Greece" },
    { from: 528, to: 528, name: "Lebanon" },
    { from: 529, to: 529, name: "Cyprus" },
    { from: 531, to: 531, name: "North Macedonia" },
    { from: 535, to: 535, name: "Malta" },
    { from: 539, to: 539, name: "Ireland" },
    { from: 540, to: 549, name: "Belgium / Luxembourg" },
    { from: 560, to: 560, name: "Portugal" },
    { from: 569, to: 569, name: "Iceland" },
    { from: 570, to: 579, name: "Denmark" },
    { from: 590, to: 590, name: "Poland" },
    { from: 594, to: 594, name: "Romania" },
    { from: 599, to: 599, name: "Hungary" },
    { from: 600, to: 601, name: "South Africa" },
    { from: 608, to: 608, name: "Bahrain" },
    { from: 611, to: 611, name: "Morocco" },
    { from: 613, to: 613, name: "Algeria" },
    { from: 616, to: 616, name: "Kenya" },
    { from: 618, to: 618, name: "Ivory Coast" },
    { from: 619, to: 619, name: "Tunisia" },
    { from: 621, to: 621, name: "Syria" },
    { from: 622, to: 622, name: "Egypt" },
    { from: 625, to: 625, name: "Jordan" },
    { from: 626, to: 626, name: "Iran" },
    { from: 627, to: 627, name: "Kuwait" },
    { from: 628, to: 628, name: "Saudi Arabia" },
    { from: 629, to: 629, name: "United Arab Emirates" },
    { from: 640, to: 649, name: "Finland" },
    { from: 690, to: 699, name: "China" },
    { from: 700, to: 709, name: "Norway" },
    { from: 729, to: 729, name: "Israel" },
    { from: 730, to: 739, name: "Sweden" },
    { from: 740, to: 745, name: "Central America" },
    { from: 746, to: 746, name: "Dominican Republic" },
    { from: 750, to: 750, name: "Mexico" },
    { from: 754, to: 755, name: "Canada" },
    { from: 759, to: 759, name: "Venezuela" },
    { from: 760, to: 769, name: "Switzerland" },
    { from: 770, to: 771, name: "Colombia" },
    { from: 773, to: 773, name: "Uruguay" },
    { from: 775, to: 775, name: "Peru" },
    { from: 777, to: 777, name: "Bolivia" },
    { from: 778, to: 779, name: "Argentina" },
    { from: 780, to: 780, name: "Chile" },
    { from: 784, to: 784, name: "Paraguay" },
    { from: 786, to: 786, name: "Ecuador" },
    { from: 789, to: 790, name: "Brazil" },
    { from: 800, to: 839, name: "Italy" },
    { from: 840, to: 849, name: "Spain" },
    { from: 850, to: 850, name: "Cuba" },
    { from: 858, to: 858, name: "Slovakia" },
    { from: 859, to: 859, name: "Czechia" },
    { from: 860, to: 860, name: "Serbia" },
    { from: 865, to: 865, name: "Mongolia" },
    { from: 867, to: 867, name: "North Korea" },
    { from: 868, to: 869, name: "Turkey" },
    { from: 870, to: 879, name: "Netherlands" },
    { from: 880, to: 881, name: "South Korea" },
    { from: 883, to: 883, name: "Myanmar" },
    { from: 884, to: 884, name: "Cambodia" },
    { from: 885, to: 885, name: "Thailand" },
    { from: 888, to: 888, name: "Singapore" },
    { from: 890, to: 890, name: "India" },
    { from: 893, to: 893, name: "Vietnam" },
    { from: 896, to: 896, name: "Pakistan" },
    { from: 899, to: 899, name: "Indonesia" },
    { from: 900, to: 919, name: "Austria" },
    { from: 930, to: 939, name: "Australia" },
    { from: 940, to: 949, name: "New Zealand" },
    { from: 955, to: 955, name: "Malaysia" },
    { from: 958, to: 958, name: "Macau" },
    { from: 977, to: 977, name: "ISSN (periodical)" },
    { from: 978, to: 979, name: "ISBN (book)" },
    { from: 980, to: 980, name: "Refund receipt" },
    { from: 981, to: 984, name: "Coupon" },
    { from: 990, to: 999, name: "Coupon" }
];

function catIsAllDigits(code) {
    if (code.length === 0) return false;
    for (var i = 0; i < code.length; i++) {
        var c = code.charAt(i);
        if (c < "0" || c > "9") return false;
    }
    return true;
}

/*
    GS1 modulo-10 check digit, as used by EAN-13, EAN-8, UPC-A and ITF-14.
    Returns true only for a length the standard defines - anything else is some
    other symbology (Code 128, QR, an internal label) and is left alone.
*/
function catCheckDigitValid(code) {
    var text = invStr(code);
    if (!catIsAllDigits(text)) return true;
    if (text.length !== 8 && text.length !== 12 && text.length !== 13 && text.length !== 14) {
        return true;
    }

    // Weights alternate 3 and 1 from the right, excluding the check digit
    var sum = 0;
    var weight = 3;
    for (var i = text.length - 2; i >= 0; i--) {
        sum += parseInt(text.charAt(i), 10) * weight;
        weight = (weight === 3) ? 1 : 3;
    }
    var expected = (10 - (sum % 10)) % 10;
    return expected === parseInt(text.charAt(text.length - 1), 10);
}

/*
    Canonical form for catalogue keys: a 12-digit UPC-A is the same product as
    the 13-digit EAN that carries a leading zero, and a scanner may report
    either. Storing and looking up the 13-digit form makes the two agree.
*/
function catNormaliseBarcode(code) {
    var text = invStr(code);
    if (text.length === 12 && catIsAllDigits(text)) return "0" + text;
    return text;
}

/* Human name for the code's issuing prefix, or "" when it is not a GS1 code */
function catBarcodeOrigin(code) {
    var text = catNormaliseBarcode(code);
    if (!catIsAllDigits(text) || text.length !== 13) return "";

    var prefix = parseInt(text.substring(0, 3), 10);
    for (var i = 0; i < CAT_GS1_PREFIXES.length; i++) {
        if (prefix >= CAT_GS1_PREFIXES[i].from && prefix <= CAT_GS1_PREFIXES[i].to) {
            return CAT_GS1_PREFIXES[i].name;
        }
    }
    return "";
}

function catLoad() {
    if (!filelib.fileExists(CAT_PATH)) return { entries: {} };
    try {
        var parsed = JSON.parse(filelib.readFile(CAT_PATH));
        if (parsed && parsed.entries && typeof parsed.entries === "object") return parsed;
    } catch (e) {}
    return { entries: {} };
}

function catSave(catalogue) {
    filelib.mkdir(INV_DIR);
    return filelib.writeFile(CAT_PATH, JSON.stringify(catalogue));
}

function catCount(catalogue) {
    var total = 0;
    for (var key in catalogue.entries) {
        if (Object.prototype.hasOwnProperty.call(catalogue.entries, key)) total++;
    }
    return total;
}

function catLookup(catalogue, barcode) {
    var key = catNormaliseBarcode(barcode);
    if (key === "") return null;
    var hit = catalogue.entries[key];
    return hit ? hit : null;
}

/*
    Records what a barcode is called. Entries taught by naming an item never
    overwrite an imported row's richer detail with blanks.
*/
function catRemember(catalogue, barcode, entry, source) {
    var key = catNormaliseBarcode(barcode);
    if (key === "" || invStr(entry.name) === "") return false;
    if (!catalogue.entries[key] && catCount(catalogue) >= CAT_CAP) return false;

    var existing = catalogue.entries[key] || {};
    catalogue.entries[key] = {
        name: invStr(entry.name),
        brand: invStr(entry.brand) || invStr(existing.brand),
        category: invStr(entry.category) || invStr(existing.category),
        unit: invStr(entry.unit) || invStr(existing.unit),
        source: source || existing.source || "local",
        updatedAt: invNow()
    };
    return true;
}

/*
    Splits one CSV line, honouring quoted fields and doubled quotes so a product
    name containing a comma survives the trip.
*/
function catSplitCsvLine(line) {
    var fields = [];
    var current = "";
    var inQuotes = false;

    for (var i = 0; i < line.length; i++) {
        var c = line.charAt(i);
        if (inQuotes) {
            if (c === "\"") {
                if (line.charAt(i + 1) === "\"") {
                    current += "\"";
                    i++;
                } else {
                    inQuotes = false;
                }
            } else {
                current += c;
            }
        } else if (c === "\"") {
            inQuotes = true;
        } else if (c === "," || c === ";" || c === "\t") {
            fields.push(current);
            current = "";
        } else {
            current += c;
        }
    }
    fields.push(current);
    return fields;
}
