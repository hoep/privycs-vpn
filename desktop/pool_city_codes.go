package main

// cityCodeToName maps the 3-letter city codes commonly used in
// commercial VPN provider hostnames to full English names.
//
// Naming convention these providers use is "<cc>-<city>-<protocol>-<n>"
// where <city> is a 3-letter IATA airport code most of the time, with
// occasional non-airport codes for cities without a major hub.
//
// Source: hostnames observed across Mullvad, IVPN, Proton, AzireVPN,
// PIA. The list is hand-curated rather than a full IATA dump because
// only ~100 codes ever show up in VPN hostnames; including all 17000+
// IATA codes would bloat binary size for zero practical benefit.
//
// Adding a new city: append the lower-case 3-letter code with the
// English city name in title case. Locale-specific variants (German
// "Wien" for Vienna) are intentionally NOT supported - the UI runs
// in English regardless of OS locale.
var cityCodeToName = map[string]string{
	// Europe - DACH
	"vie": "Vienna",
	"fra": "Frankfurt",
	"ber": "Berlin",
	"muc": "Munich",
	"dus": "Düsseldorf",
	"ham": "Hamburg",
	"zrh": "Zurich",
	"gva": "Geneva",

	// Europe - West
	"par": "Paris",
	"mrs": "Marseille",
	"lon": "London",
	"mnc": "Manchester",
	"glw": "Glasgow",
	"mad": "Madrid",
	"bcn": "Barcelona",
	"mil": "Milan",
	"rom": "Rome",
	"ams": "Amsterdam",
	"bru": "Brussels",

	// Europe - Nordic
	"sto": "Stockholm",
	"got": "Gothenburg",
	"mma": "Malmö",
	"osl": "Oslo",
	"cph": "Copenhagen",
	"hel": "Helsinki",

	// Europe - East
	"prg": "Prague",
	"war": "Warsaw",
	"buh": "Bucharest",
	"sof": "Sofia",
	"bud": "Budapest",
	"ath": "Athens",
	"lis": "Lisbon",
	"dub": "Dublin",
	"tll": "Tallinn",
	"rix": "Riga",
	"vno": "Vilnius",
	"beg": "Belgrade",
	"zag": "Zagreb",
	"lju": "Ljubljana",
	"bts": "Bratislava",
	"kiv": "Kyiv",

	// North America - US
	"nyc": "New York",
	"chi": "Chicago",
	"lax": "Los Angeles",
	"sea": "Seattle",
	"sjc": "San Jose",
	"mia": "Miami",
	"dal": "Dallas",
	"den": "Denver",
	"atl": "Atlanta",
	"phx": "Phoenix",
	"bos": "Boston",
	"iad": "Washington",
	"slc": "Salt Lake City",

	// North America - Canada / Mexico
	"yyz": "Toronto",
	"yvr": "Vancouver",
	"ymq": "Montreal",
	"mex": "Mexico City",

	// South America
	"sao": "São Paulo",
	"gru": "São Paulo",
	"eze": "Buenos Aires",
	"scl": "Santiago",
	"bog": "Bogotá",
	"lim": "Lima",

	// Asia - East
	"tok": "Tokyo",
	"nrt": "Tokyo",
	"osa": "Osaka",
	"sel": "Seoul",
	"icn": "Seoul",
	"hkg": "Hong Kong",
	"tpe": "Taipei",

	// Asia - South-East
	"sin": "Singapore",
	"kul": "Kuala Lumpur",
	"bkk": "Bangkok",
	"jkt": "Jakarta",
	"mnl": "Manila",
	"hnd": "Hanoi",
	"sgn": "Ho Chi Minh City",

	// Asia - South
	"bom": "Mumbai",
	"del": "Delhi",
	"blr": "Bangalore",

	// Oceania
	"syd": "Sydney",
	"mel": "Melbourne",
	"per": "Perth",
	"akl": "Auckland",

	// Africa
	"jnb": "Johannesburg",
	"cpt": "Cape Town",
	"lag": "Lagos",
	"nai": "Nairobi",
	"cai": "Cairo",

	// Middle East
	"dxb": "Dubai",
	"tlv": "Tel Aviv",
	"ist": "Istanbul",
}
