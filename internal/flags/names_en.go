package flags

// 本文件由 lipis/flag-icons 上游 country.json 自动生成（生成器未入库）。
// 规则：英文名转小写 → 重音字母折叠为 ASCII → 去掉所有非字母数字字符。
// 与 names_zh.go 中的 zhNames 一一对应，供 Normalize 识别英文输入。

// enNames：英文名称（已折叠）→ ISO 3166-1 alpha-2 大写代码。
var enNames = map[string]string{
	"andorra":                                "AD", // Andorra
	"unitedarabemirates":                     "AE", // United Arab Emirates
	"afghanistan":                            "AF", // Afghanistan
	"antiguaandbarbuda":                      "AG", // Antigua and Barbuda
	"anguilla":                               "AI", // Anguilla
	"albania":                                "AL", // Albania
	"armenia":                                "AM", // Armenia
	"angola":                                 "AO", // Angola
	"antarctica":                             "AQ", // Antarctica
	"argentina":                              "AR", // Argentina
	"americansamoa":                          "AS", // American Samoa
	"austria":                                "AT", // Austria
	"australia":                              "AU", // Australia
	"aruba":                                  "AW", // Aruba
	"alandislands":                           "AX", // Aland Islands
	"azerbaijan":                             "AZ", // Azerbaijan
	"bosniaandherzegovina":                   "BA", // Bosnia and Herzegovina
	"barbados":                               "BB", // Barbados
	"bangladesh":                             "BD", // Bangladesh
	"belgium":                                "BE", // Belgium
	"burkinafaso":                            "BF", // Burkina Faso
	"bulgaria":                               "BG", // Bulgaria
	"bahrain":                                "BH", // Bahrain
	"burundi":                                "BI", // Burundi
	"benin":                                  "BJ", // Benin
	"saintbarthelemy":                        "BL", // Saint Barthélemy
	"bermuda":                                "BM", // Bermuda
	"bruneidarussalam":                       "BN", // Brunei Darussalam
	"bolivia":                                "BO", // Bolivia
	"bonairesinteustatiusandsaba":            "BQ", // Bonaire, Sint Eustatius and Saba
	"brazil":                                 "BR", // Brazil
	"bahamas":                                "BS", // Bahamas
	"bhutan":                                 "BT", // Bhutan
	"bouvetisland":                           "BV", // Bouvet Island
	"botswana":                               "BW", // Botswana
	"belarus":                                "BY", // Belarus
	"belize":                                 "BZ", // Belize
	"canada":                                 "CA", // Canada
	"cocoskeelingislands":                    "CC", // Cocos (Keeling) Islands
	"democraticrepublicofthecongo":           "CD", // Democratic Republic of the Congo
	"centralafricanrepublic":                 "CF", // Central African Republic
	"republicofthecongo":                     "CG", // Republic of the Congo
	"switzerland":                            "CH", // Switzerland
	"cotedivoire":                            "CI", // Côte d'Ivoire
	"cookislands":                            "CK", // Cook Islands
	"chile":                                  "CL", // Chile
	"cameroon":                               "CM", // Cameroon
	"china":                                  "CN", // China
	"colombia":                               "CO", // Colombia
	"costarica":                              "CR", // Costa Rica
	"cuba":                                   "CU", // Cuba
	"caboverde":                              "CV", // Cabo Verde
	"curacao":                                "CW", // Curaçao
	"christmasisland":                        "CX", // Christmas Island
	"cyprus":                                 "CY", // Cyprus
	"czechrepublic":                          "CZ", // Czech Republic
	"germany":                                "DE", // Germany
	"djibouti":                               "DJ", // Djibouti
	"denmark":                                "DK", // Denmark
	"dominica":                               "DM", // Dominica
	"dominicanrepublic":                      "DO", // Dominican Republic
	"algeria":                                "DZ", // Algeria
	"ecuador":                                "EC", // Ecuador
	"estonia":                                "EE", // Estonia
	"egypt":                                  "EG", // Egypt
	"westernsahara":                          "EH", // Western Sahara
	"eritrea":                                "ER", // Eritrea
	"spain":                                  "ES", // Spain
	"ethiopia":                               "ET", // Ethiopia
	"finland":                                "FI", // Finland
	"fiji":                                   "FJ", // Fiji
	"falklandislands":                        "FK", // Falkland Islands
	"federatedstatesofmicronesia":            "FM", // Federated States of Micronesia
	"faroeislands":                           "FO", // Faroe Islands
	"france":                                 "FR", // France
	"gabon":                                  "GA", // Gabon
	"unitedkingdom":                          "GB", // United Kingdom
	"grenada":                                "GD", // Grenada
	"georgia":                                "GE", // Georgia
	"frenchguiana":                           "GF", // French Guiana
	"guernsey":                               "GG", // Guernsey
	"ghana":                                  "GH", // Ghana
	"gibraltar":                              "GI", // Gibraltar
	"greenland":                              "GL", // Greenland
	"gambia":                                 "GM", // Gambia
	"guinea":                                 "GN", // Guinea
	"guadeloupe":                             "GP", // Guadeloupe
	"equatorialguinea":                       "GQ", // Equatorial Guinea
	"greece":                                 "GR", // Greece
	"southgeorgiaandthesouthsandwichislands": "GS", // South Georgia and the South Sandwich Islands
	"guatemala":                              "GT", // Guatemala
	"guam":                                   "GU", // Guam
	"guineabissau":                           "GW", // Guinea-Bissau
	"guyana":                                 "GY", // Guyana
	"hongkong":                               "HK", // Hong Kong
	"heardislandandmcdonaldislands":          "HM", // Heard Island and McDonald Islands
	"honduras":                               "HN", // Honduras
	"croatia":                                "HR", // Croatia
	"haiti":                                  "HT", // Haiti
	"hungary":                                "HU", // Hungary
	"indonesia":                              "ID", // Indonesia
	"ireland":                                "IE", // Ireland
	"israel":                                 "IL", // Israel
	"isleofman":                              "IM", // Isle of Man
	"india":                                  "IN", // India
	"britishindianoceanterritory":            "IO", // British Indian Ocean Territory
	"iraq":                                   "IQ", // Iraq
	"iran":                                   "IR", // Iran
	"iceland":                                "IS", // Iceland
	"italy":                                  "IT", // Italy
	"jersey":                                 "JE", // Jersey
	"jamaica":                                "JM", // Jamaica
	"jordan":                                 "JO", // Jordan
	"japan":                                  "JP", // Japan
	"kenya":                                  "KE", // Kenya
	"kyrgyzstan":                             "KG", // Kyrgyzstan
	"cambodia":                               "KH", // Cambodia
	"kiribati":                               "KI", // Kiribati
	"comoros":                                "KM", // Comoros
	"saintkittsandnevis":                     "KN", // Saint Kitts and Nevis
	"northkorea":                             "KP", // North Korea
	"southkorea":                             "KR", // South Korea
	"kuwait":                                 "KW", // Kuwait
	"caymanislands":                          "KY", // Cayman Islands
	"kazakhstan":                             "KZ", // Kazakhstan
	"laos":                                   "LA", // Laos
	"lebanon":                                "LB", // Lebanon
	"saintlucia":                             "LC", // Saint Lucia
	"liechtenstein":                          "LI", // Liechtenstein
	"srilanka":                               "LK", // Sri Lanka
	"liberia":                                "LR", // Liberia
	"lesotho":                                "LS", // Lesotho
	"lithuania":                              "LT", // Lithuania
	"luxembourg":                             "LU", // Luxembourg
	"latvia":                                 "LV", // Latvia
	"libya":                                  "LY", // Libya
	"morocco":                                "MA", // Morocco
	"monaco":                                 "MC", // Monaco
	"moldova":                                "MD", // Moldova
	"montenegro":                             "ME", // Montenegro
	"saintmartin":                            "MF", // Saint Martin
	"madagascar":                             "MG", // Madagascar
	"marshallislands":                        "MH", // Marshall Islands
	"northmacedonia":                         "MK", // North Macedonia
	"mali":                                   "ML", // Mali
	"myanmar":                                "MM", // Myanmar
	"mongolia":                               "MN", // Mongolia
	"macau":                                  "MO", // Macau
	"northernmarianaislands":                 "MP", // Northern Mariana Islands
	"martinique":                             "MQ", // Martinique
	"mauritania":                             "MR", // Mauritania
	"montserrat":                             "MS", // Montserrat
	"malta":                                  "MT", // Malta
	"mauritius":                              "MU", // Mauritius
	"maldives":                               "MV", // Maldives
	"malawi":                                 "MW", // Malawi
	"mexico":                                 "MX", // Mexico
	"malaysia":                               "MY", // Malaysia
	"mozambique":                             "MZ", // Mozambique
	"namibia":                                "NA", // Namibia
	"newcaledonia":                           "NC", // New Caledonia
	"niger":                                  "NE", // Niger
	"norfolkisland":                          "NF", // Norfolk Island
	"nigeria":                                "NG", // Nigeria
	"nicaragua":                              "NI", // Nicaragua
	"netherlands":                            "NL", // Netherlands
	"norway":                                 "NO", // Norway
	"nepal":                                  "NP", // Nepal
	"nauru":                                  "NR", // Nauru
	"niue":                                   "NU", // Niue
	"newzealand":                             "NZ", // New Zealand
	"oman":                                   "OM", // Oman
	"panama":                                 "PA", // Panama
	"peru":                                   "PE", // Peru
	"frenchpolynesia":                        "PF", // French Polynesia
	"papuanewguinea":                         "PG", // Papua New Guinea
	"philippines":                            "PH", // Philippines
	"pakistan":                               "PK", // Pakistan
	"poland":                                 "PL", // Poland
	"saintpierreandmiquelon":                 "PM", // Saint Pierre and Miquelon
	"pitcairn":                               "PN", // Pitcairn
	"puertorico":                             "PR", // Puerto Rico
	"stateofpalestine":                       "PS", // State of Palestine
	"portugal":                               "PT", // Portugal
	"palau":                                  "PW", // Palau
	"paraguay":                               "PY", // Paraguay
	"qatar":                                  "QA", // Qatar
	"reunion":                                "RE", // Réunion
	"romania":                                "RO", // Romania
	"serbia":                                 "RS", // Serbia
	"russia":                                 "RU", // Russia
	"rwanda":                                 "RW", // Rwanda
	"saudiarabia":                            "SA", // Saudi Arabia
	"solomonislands":                         "SB", // Solomon Islands
	"seychelles":                             "SC", // Seychelles
	"sudan":                                  "SD", // Sudan
	"sweden":                                 "SE", // Sweden
	"singapore":                              "SG", // Singapore
	"sainthelenaascensionandtristandacunha":  "SH", // Saint Helena, Ascension and Tristan da Cunha
	"slovenia":                               "SI", // Slovenia
	"svalbardandjanmayen":                    "SJ", // Svalbard and Jan Mayen
	"slovakia":                               "SK", // Slovakia
	"sierraleone":                            "SL", // Sierra Leone
	"sanmarino":                              "SM", // San Marino
	"senegal":                                "SN", // Senegal
	"somalia":                                "SO", // Somalia
	"suriname":                               "SR", // Suriname
	"southsudan":                             "SS", // South Sudan
	"saotomeandprincipe":                     "ST", // Sao Tome and Principe
	"elsalvador":                             "SV", // El Salvador
	"sintmaarten":                            "SX", // Sint Maarten
	"syria":                                  "SY", // Syria
	"eswatini":                               "SZ", // Eswatini
	"turksandcaicosislands":                  "TC", // Turks and Caicos Islands
	"chad":                                   "TD", // Chad
	"frenchsouthernterritories":              "TF", // French Southern Territories
	"togo":                                   "TG", // Togo
	"thailand":                               "TH", // Thailand
	"tajikistan":                             "TJ", // Tajikistan
	"tokelau":                                "TK", // Tokelau
	"timorleste":                             "TL", // Timor-Leste
	"turkmenistan":                           "TM", // Turkmenistan
	"tunisia":                                "TN", // Tunisia
	"tonga":                                  "TO", // Tonga
	"turkiye":                                "TR", // Türkiye
	"trinidadandtobago":                      "TT", // Trinidad and Tobago
	"tuvalu":                                 "TV", // Tuvalu
	"taiwan":                                 "TW", // Taiwan
	"tanzania":                               "TZ", // Tanzania
	"ukraine":                                "UA", // Ukraine
	"uganda":                                 "UG", // Uganda
	"unitedstatesminoroutlyingislands":       "UM", // United States Minor Outlying Islands
	"unitedstatesofamerica":                  "US", // United States of America
	"uruguay":                                "UY", // Uruguay
	"uzbekistan":                             "UZ", // Uzbekistan
	"holysee":                                "VA", // Holy See
	"saintvincentandthegrenadines":           "VC", // Saint Vincent and the Grenadines
	"venezuela":                              "VE", // Venezuela
	"virginislandsbritish":                   "VG", // Virgin Islands (British)
	"virginislandsus":                        "VI", // Virgin Islands (U.S.)
	"vietnam":                                "VN", // Vietnam
	"vanuatu":                                "VU", // Vanuatu
	"wallisandfutuna":                        "WF", // Wallis and Futuna
	"samoa":                                  "WS", // Samoa
	"yemen":                                  "YE", // Yemen
	"mayotte":                                "YT", // Mayotte
	"southafrica":                            "ZA", // South Africa
	"zambia":                                 "ZM", // Zambia
	"zimbabwe":                               "ZW", // Zimbabwe
	"europeanunion":                          "EU", // European Union
	"europe":                                 "EU", // Europe
}
