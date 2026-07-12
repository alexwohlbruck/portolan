package atlas

// Editor bootstrap data, injected into editor.html at serve time (the HTML
// carries %LOCS% / %SWATCHES% placeholders — unfilled they are a JS syntax
// error and the whole editor dies black).

// locsJSON: [key, name, lat, lon, feed] — the named test locations
// (docs/LESSONS.md); camera bookmarks only, the network is one continuous
// drawing per feed.
const locsJSON = `[["chi_loop", "Chicago Loop", 41.8799, -87.6321, "29"], ["essex", "Essex · Bowery · 2 Av", 40.72111792489585, -73.9891109124895, "5"], ["dekalb", "DeKalb · Atlantic Av", 40.6869073379375, -73.9787737257991, "5"], ["franklin", "Franklin Av 2·3·4·5 / S", 40.670784818489494, -73.95851625957265, "5"], ["gc149", "149 St–Grand Concourse", 40.818519934487654, -73.92737345665779, "5"], ["chi_loop_nw", "Chicago Loop NW (Lake/Wells 4-way)", 41.8857, -87.6343, "29"], ["schermerhorn", "Hoyt–Schermerhorn (kink)", 40.689702952298774, -73.98801104593792, "5"], ["park_place", "Park Place 1·2·3 (turn attaches wrong way)", 40.71393491072925, -74.01042911606714, "5"], ["delancey", "Delancey · Chrystie (J/Z × B/D)", 40.71877, -73.99275, "5"], ["times_sq", "Times Sq — 1·2·3 spur to S shuttle", 40.7554445689446, -73.9868599558424, "5"], ["seventh_57", "7 Av · 57 St (low-zoom wrong-way attach)", 40.76585363423274, -73.97986990880315, "5"], ["sixth_53", "6 Av · 53 St", 40.7617843177338, -73.97905994761575, "5"], ["eighth_53", "8 Av · 53 St", 40.76416458226208, -73.98471345247675, "5"], ["fulton_cityhall", "4·5 Fulton ↔ City Hall", 40.71184547317185, -74.00658655830976, "5"], ["rector", "Rector St (1 × 4·5 kiss)", 40.7075, -74.0137, "5"], ["bklyn_bridge", "Bklyn Bridge–City Hall (J/Z × 4·5·6 weave)", 40.7124, -74.0045, "5"], ["borough_hall", "Borough Hall (2·3 × 4·5 × R + A/C S-kink)", 40.6921, -73.9888, "5"], ["bway_junction", "Broadway Junction (J/Z bends into L)", 40.678808313065076, -73.9033091784346, "5"], ["canal_ace", "Canal St A·C·E (tight bend, keep it)", 40.72272, -74.00529, "5"]]`

// swatchesJSON: per-feed route palettes, true route_color hexes.
const swatchesJSON = `{"5": [["D82233", "1·2·3"], ["0062CF", "A·C·E"], ["EB6800", "B·D·F·M"], ["F6BC26", "N·Q·R·W"], ["009952", "4·5·6"], ["8E5C33", "J·Z"], ["7C858C", "L·S"], ["9A38A1", "7"], ["799534", "G"], ["08179C", "SIR"]], "29": [["C60C30", "Red"], ["00A1DE", "Blue"], ["62361B", "Brown"], ["522398", "Purple"], ["E27EA6", "Pink"], ["F9461C", "Orange"], ["009B3A", "Green"], ["F9E300", "Yellow"]]}`
