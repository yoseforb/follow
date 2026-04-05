//go:build integration

package integration_test

import (
	"os"
	"testing"
)

// seedRoutesHebrew is the full list of 60 Hebrew routes to seed.
var seedRoutesHebrew = []seedRoute{
	// בית חולים איכילוב — 15 routes
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "קרדיולוגיה, בניין ב׳, קומה 3",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "קרדיולוגיה, בניין ב׳, קומה 3" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "חניון תת-קרקעי P2",
		EndPoint:     "חדר מיון",
		Description: "ניווט מ" + "חניון תת-קרקעי P2" +
			" אל " + "חדר מיון" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "רדיולוגיה, בניין א׳, קומה -1",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "רדיולוגיה, בניין א׳, קומה -1" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "שער 3 (רחוב ויצמן)",
		EndPoint:     "מחלקת יולדות, קומה 5",
		Description: "ניווט מ" + "שער 3 (רחוב ויצמן)" +
			" אל " + "מחלקת יולדות, קומה 5" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "חניון תת-קרקעי P1",
		EndPoint:     "אורתופדיה, בניין ג׳, קומה 2",
		Description: "ניווט מ" + "חניון תת-קרקעי P1" +
			" אל " + "אורתופדיה, בניין ג׳, קומה 2" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "כניסת מיון",
		EndPoint:     "טיפול נמרץ, בניין א׳, קומה 4",
		Description: "ניווט מ" + "כניסת מיון" +
			" אל " + "טיפול נמרץ, בניין א׳, קומה 4" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "קפיטריה",
		EndPoint:     "אונקולוגיה יום, בניין ד׳",
		Description: "ניווט מ" + "קפיטריה" +
			" אל " + "אונקולוגיה יום, בניין ד׳" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "מעבדת דם, קומה -1",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "מעבדת דם, קומה -1" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "שער 2 (רחוב דובנוב)",
		EndPoint:     "נוירולוגיה, בניין ב׳, קומה 6",
		Description: "ניווט מ" + "שער 2 (רחוב דובנוב)" +
			" אל " + "נוירולוגיה, בניין ב׳, קומה 6" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "חניון תת-קרקעי P1",
		EndPoint:     "מרפאת עיניים, בניין ה׳",
		Description: "ניווט מ" + "חניון תת-קרקעי P1" +
			" אל " + "מרפאת עיניים, בניין ה׳" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "מיון ילדים, בניין א׳",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "מיון ילדים, בניין א׳" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "שער 3 (רחוב ויצמן)",
		EndPoint:     "יחידת דיאליזה, קומה 2",
		Description: "ניווט מ" + "שער 3 (רחוב ויצמן)" +
			" אל " + "יחידת דיאליזה, קומה 2" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "חניון מבקרים",
		EndPoint:     "מרכז פיזיותרפיה",
		Description: "ניווט מ" + "חניון מבקרים" +
			" אל " + "מרכז פיזיותרפיה" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "בית מרקחת, קומת קרקע",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "בית מרקחת, קומת קרקע" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב ויצמן 6, תל אביב",
		LocationName: "בית חולים איכילוב",
		StartPoint:   "כניסת מיון",
		EndPoint:     "מרכז טראומה, בניין א׳",
		Description: "ניווט מ" + "כניסת מיון" +
			" אל " + "מרכז טראומה, בניין א׳" +
			" ב" + "בית חולים איכילוב",
		WaypointCount: 3,
	},

	// המרכז הרפואי שיבא — 10 routes
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "שער ראשי",
		EndPoint:     "מרכז הלב, בניין 44",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "מרכז הלב, בניין 44" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 3,
	},
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "חניון ב׳",
		EndPoint:     "בית החולים לילדים",
		Description: "ניווט מ" + "חניון ב׳" +
			" אל " + "בית החולים לילדים" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 2,
	},
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "כניסת תחנת אוטובוס",
		EndPoint:     "מרכז שיקום",
		Description: "ניווט מ" + "כניסת תחנת אוטובוס" +
			" אל " + "מרכז שיקום" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 2,
	},
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "כניסת מיון",
		EndPoint:     "נוירולוגיה, בניין 12",
		Description: "ניווט מ" + "כניסת מיון" +
			" אל " + "נוירולוגיה, בניין 12" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 3,
	},
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "שער ראשי",
		EndPoint:     "מרכז אונקולוגי",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "מרכז אונקולוגי" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 2,
	},
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "חניון א׳",
		EndPoint:     "יחידת דיאליזה, בניין 8",
		Description: "ניווט מ" + "חניון א׳" +
			" אל " + "יחידת דיאליזה, בניין 8" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 2,
	},
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "שער דרומי",
		EndPoint:     "מגדל המחקר",
		Description: "ניווט מ" + "שער דרומי" +
			" אל " + "מגדל המחקר" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 3,
	},
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "שער ראשי",
		EndPoint:     "מרכז MRI, בניין 17",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "מרכז MRI, בניין 17" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 2,
	},
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "כניסת מיון",
		EndPoint:     "יחידת כוויות",
		Description: "ניווט מ" + "כניסת מיון" +
			" אל " + "יחידת כוויות" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 2,
	},
	{
		Address:      "דרך שיבא 2, רמת גן",
		LocationName: "המרכז הרפואי שיבא",
		StartPoint:   "חניון ג׳",
		EndPoint:     "אגף פסיכיאטרי",
		Description: "ניווט מ" + "חניון ג׳" +
			" אל " + "אגף פסיכיאטרי" +
			" ב" + "המרכז הרפואי שיבא",
		WaypointCount: 2,
	},

	// הדסה עין כרם — 10 routes
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "מחלקה כירורגית, קומה 5",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "מחלקה כירורגית, קומה 5" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "חניון תת-קרקעי",
		EndPoint:     "הבניין העגול, אונקולוגיה",
		Description: "ניווט מ" + "חניון תת-קרקעי" +
			" אל " + "הבניין העגול, אונקולוגיה" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "כניסת מיון",
		EndPoint:     "מרכז אם ותינוק",
		Description: "ניווט מ" + "כניסת מיון" +
			" אל " + "מרכז אם ותינוק" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "כניסת מבקרים",
		EndPoint:     "בית הכנסת — חלונות שאגאל",
		Description: "ניווט מ" + "כניסת מבקרים" +
			" אל " + "בית הכנסת — חלונות שאגאל" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "קרדיולוגיה, קומה 3",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "קרדיולוגיה, קומה 3" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "חניון תת-קרקעי",
		EndPoint:     "מכון עיניים",
		Description: "ניווט מ" + "חניון תת-קרקעי" +
			" אל " + "מכון עיניים" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "כניסת מיון",
		EndPoint:     "מיון ילדים",
		Description: "ניווט מ" + "כניסת מיון" +
			" אל " + "מיון ילדים" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "שער ב׳",
		EndPoint:     "מרכז שיקום",
		Description: "ניווט מ" + "שער ב׳" +
			" אל " + "מרכז שיקום" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "השתלת מח עצם, קומה 7",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "השתלת מח עצם, קומה 7" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב קלמן יעקב מן, ירושלים",
		LocationName: "הדסה עין כרם",
		StartPoint:   "כניסת מבקרים",
		EndPoint:     "בית תפילה וגן",
		Description: "ניווט מ" + "כניסת מבקרים" +
			" אל " + "בית תפילה וגן" +
			" ב" + "הדסה עין כרם",
		WaypointCount: 2,
	},

	// רמב״ם — המרכז הרפואי — 10 routes
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "שער ראשי",
		EndPoint:     "בית חולים תת-קרקעי לחירום",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "בית חולים תת-קרקעי לחירום" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "חניון קומות",
		EndPoint:     "רפואת ילדים, בניין ב׳",
		Description: "ניווט מ" + "חניון קומות" +
			" אל " + "רפואת ילדים, בניין ב׳" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "כניסה דרומית",
		EndPoint:     "אורתופדיה, קומה 3",
		Description: "ניווט מ" + "כניסה דרומית" +
			" אל " + "אורתופדיה, קומה 3" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "שער ראשי",
		EndPoint:     "מגדל קרדיולוגיה, קומה 7",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "מגדל קרדיולוגיה, קומה 7" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "כניסה צפונית",
		EndPoint:     "נוירוכירורגיה, בניין א׳",
		Description: "ניווט מ" + "כניסה צפונית" +
			" אל " + "נוירוכירורגיה, בניין א׳" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "חניון קומות",
		EndPoint:     "מרכז בריאות האישה",
		Description: "ניווט מ" + "חניון קומות" +
			" אל " + "מרכז בריאות האישה" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "שער ראשי",
		EndPoint:     "אגף סמי עופר המבוצר",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "אגף סמי עופר המבוצר" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 3,
	},
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "כניסת מיון",
		EndPoint:     "יחידת טראומה",
		Description: "ניווט מ" + "כניסת מיון" +
			" אל " + "יחידת טראומה" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "כניסה דרומית",
		EndPoint:     "מרפאת עור",
		Description: "ניווט מ" + "כניסה דרומית" +
			" אל " + "מרפאת עור" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 2,
	},
	{
		Address:      "רחוב העלייה השנייה 8, חיפה",
		LocationName: "רמב״ם — המרכז הרפואי",
		StartPoint:   "שער ראשי",
		EndPoint:     "בית החולים מאייר לילדים",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "בית החולים מאייר לילדים" +
			" ב" + "רמב״ם — המרכז הרפואי",
		WaypointCount: 3,
	},

	// מסלולים עצמאיים — 15 routes
	{
		Address:      "דיזנגוף סנטר, תל אביב",
		LocationName: "דיזנגוף סנטר",
		StartPoint:   "כניסה מרמת הרחוב",
		EndPoint:     "קולנוע, קומה 3",
		Description: "ניווט מ" + "כניסה מרמת הרחוב" +
			" אל " + "קולנוע, קומה 3" +
			" ב" + "דיזנגוף סנטר",
		WaypointCount: 2,
	},
	{
		Address:      "נמל התעופה בן גוריון, טרמינל 3",
		LocationName: "נמל התעופה בן גוריון",
		StartPoint:   "אולם הגעה",
		EndPoint:     "שער C12",
		Description: "ניווט מ" + "אולם הגעה" +
			" אל " + "שער C12" +
			" ב" + "נמל התעופה בן גוריון",
		WaypointCount: 3,
	},
	{
		Address:      "האוניברסיטה העברית, הר הצופים, ירושלים",
		LocationName: "האוניברסיטה העברית",
		StartPoint:   "שער ראשי",
		EndPoint:     "הפקולטה למשפטים, בניין 4",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "הפקולטה למשפטים, בניין 4" +
			" ב" + "האוניברסיטה העברית",
		WaypointCount: 2,
	},
	{
		Address:      "קניון עזריאלי, דרך מנחם בגין, תל אביב",
		LocationName: "מרכז עזריאלי",
		StartPoint:   "חניון קומה -3",
		EndPoint:     "פודקורט, המגדל העגול",
		Description: "ניווט מ" + "חניון קומה -3" +
			" אל " + "פודקורט, המגדל העגול" +
			" ב" + "מרכז עזריאלי",
		WaypointCount: 2,
	},
	{
		Address:      "תחנה מרכזית, ירושלים",
		LocationName: "תחנה מרכזית ירושלים",
		StartPoint:   "קומת רציפים",
		EndPoint:     "יציאה לרחוב יפו",
		Description: "ניווט מ" + "קומת רציפים" +
			" אל " + "יציאה לרחוב יפו" +
			" ב" + "תחנה מרכזית ירושלים",
		WaypointCount: 2,
	},
	{
		Address:      "מרכז רפואי רבין, פתח תקווה",
		LocationName: "בית חולים בילינסון",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "אגף קרדיולוגיה",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "אגף קרדיולוגיה" +
			" ב" + "בית חולים בילינסון",
		WaypointCount: 3,
	},
	{
		Address:      "מרכז רפואי אסותא, אשדוד",
		LocationName: "אסותא אשדוד",
		StartPoint:   "כניסת חניון",
		EndPoint:     "ניתוחי יום, קומה 2",
		Description: "ניווט מ" + "כניסת חניון" +
			" אל " + "ניתוחי יום, קומה 2" +
			" ב" + "אסותא אשדוד",
		WaypointCount: 2,
	},
	{
		Address:      "גרנד קניון, חיפה",
		LocationName: "גרנד קניון חיפה",
		StartPoint:   "כניסה צפונית",
		EndPoint:     "סינמה סיטי, קומה 3",
		Description: "ניווט מ" + "כניסה צפונית" +
			" אל " + "סינמה סיטי, קומה 3" +
			" ב" + "גרנד קניון חיפה",
		WaypointCount: 2,
	},
	{
		Address:      "הטכניון, חיפה",
		LocationName: "הטכניון",
		StartPoint:   "שער ראשי",
		EndPoint:     "בניין מדעי המחשב",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "בניין מדעי המחשב" +
			" ב" + "הטכניון",
		WaypointCount: 3,
	},
	{
		Address:      "אוניברסיטת תל אביב, רמת אביב",
		LocationName: "אוניברסיטת תל אביב",
		StartPoint:   "שער 2 (חיים לבנון)",
		EndPoint:     "הפקולטה להנדסה",
		Description: "ניווט מ" + "שער 2 (חיים לבנון)" +
			" אל " + "הפקולטה להנדסה" +
			" ב" + "אוניברסיטת תל אביב",
		WaypointCount: 2,
	},
	{
		Address:      "קניון מלחה, ירושלים",
		LocationName: "קניון ירושלים",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "באולינג, קומה -1",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "באולינג, קומה -1" +
			" ב" + "קניון ירושלים",
		WaypointCount: 2,
	},
	{
		Address:      "איקאה נתניה",
		LocationName: "איקאה נתניה",
		StartPoint:   "חניון",
		EndPoint:     "מסעדה וקפה",
		Description: "ניווט מ" + "חניון" +
			" אל " + "מסעדה וקפה" +
			" ב" + "איקאה נתניה",
		WaypointCount: 2,
	},
	{
		Address:      "מרכז רפואי סורוקה, באר שבע",
		LocationName: "בית חולים סורוקה",
		StartPoint:   "כניסה ראשית",
		EndPoint:     "מחלקת ילדים",
		Description: "ניווט מ" + "כניסה ראשית" +
			" אל " + "מחלקת ילדים" +
			" ב" + "בית חולים סורוקה",
		WaypointCount: 3,
	},
	{
		Address:      "מרכז רפואי כרמל, חיפה",
		LocationName: "בית חולים כרמל",
		StartPoint:   "שער ראשי",
		EndPoint:     "מחלקת יולדות",
		Description: "ניווט מ" + "שער ראשי" +
			" אל " + "מחלקת יולדות" +
			" ב" + "בית חולים כרמל",
		WaypointCount: 2,
	},
	{
		Address:      "שרונה מרקט, תל אביב",
		LocationName: "שרונה מרקט",
		StartPoint:   "כניסה צפונית",
		EndPoint:     "אולם האוכל הפנימי",
		Description: "ניווט מ" + "כניסה צפונית" +
			" אל " + "אולם האוכל הפנימי" +
			" ב" + "שרונה מרקט",
		WaypointCount: 2,
	},
}

// TestSeedSearchDataHebrew generates 60 published public routes with
// Hebrew text across 6 anonymous users for manual testing of the
// Flutter search/discovery UI with RTL content. Skipped unless
// SEED_SEARCH_DATA_HEBREW=true is set.
func TestSeedSearchDataHebrew(t *testing.T) {
	if os.Getenv("SEED_SEARCH_DATA_HEBREW") != "true" {
		t.Skip(
			"SEED_SEARCH_DATA_HEBREW not set" +
				" — skipping Hebrew seed data generation",
		)
	}

	const numUsers = 6

	totalRoutes := len(seedRoutesHebrew)

	type seedUser struct {
		id    string
		token string
	}

	users := make([]seedUser, numUsers)
	for i := range users {
		id, token := createAnonymousUser(t)
		users[i] = seedUser{id: id, token: token}
		t.Logf("created user %d: %s", i+1, id)
	}

	valkeyClient := newValkeyClient(t)

	imageIndex := 0

	for i, route := range seedRoutesHebrew {
		u := users[i%numUsers]
		routeNum := i + 1

		t.Logf(
			"[%d/%d] creating: %s → %s at %s",
			routeNum,
			totalRoutes,
			route.StartPoint,
			route.EndPoint,
			route.LocationName,
		)

		imageIndex = seedSingleRoute(
			t, route, u.token, valkeyClient,
			imageIndex, routeNum,
		)
	}

	t.Logf(
		"seed complete: %d routes published across %d users",
		totalRoutes,
		numUsers,
	)
}
