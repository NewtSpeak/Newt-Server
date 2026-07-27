package activityapi

// 内置游戏目录（封面优先 Steam CDN header；无 appid 时可用静态图床 URL）。
// 客户端也会内置同构数据；服务端热更新时以此为准。

type gameEntry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Aliases      []string `json:"aliases,omitempty"`
	Executables  []string `json:"executables,omitempty"`
	SteamAppID   string   `json:"steam_app_id,omitempty"`
	CoverURL     string   `json:"cover_url,omitempty"`
}

func steamHeader(appID string) string {
	if appID == "" {
		return ""
	}
	return "https://cdn.cloudflare.steamstatic.com/steam/apps/" + appID + "/header.jpg"
}

func builtInCatalog() []gameEntry {
	entries := []gameEntry{
		{ID: "genshin", Name: "原神", Aliases: []string{"Genshin Impact", "Genshin"}, Executables: []string{"yuanshen.exe", "genshinimpact.exe", "genshinimpact", "yuanshen"}, SteamAppID: "1443500"},
		{ID: "starrail", Name: "崩坏：星穹铁道", Aliases: []string{"Honkai: Star Rail", "Star Rail"}, Executables: []string{"starrail.exe", "starrail"}, SteamAppID: "2195250"},
		{ID: "zzz", Name: "绝区零", Aliases: []string{"Zenless Zone Zero", "ZZZ"}, Executables: []string{"zenlesszonezero.exe", "zenlesszonezero"}, SteamAppID: "3159330"},
		{ID: "valorant", Name: "VALORANT", Aliases: []string{"无畏契约", "Valorant"}, Executables: []string{"valorant.exe", "valorant-win64-shipping.exe", "valorant"}, SteamAppID: ""},
		{ID: "lol", Name: "英雄联盟", Aliases: []string{"League of Legends", "LoL"}, Executables: []string{"league of legends.exe", "leagueclient.exe", "leagueclient"}, SteamAppID: ""},
		{ID: "cs2", Name: "Counter-Strike 2", Aliases: []string{"CS2", "CS:GO", "反恐精英"}, Executables: []string{"cs2.exe", "cs2"}, SteamAppID: "730"},
		{ID: "dota2", Name: "Dota 2", Aliases: []string{"刀塔"}, Executables: []string{"dota2.exe", "dota2"}, SteamAppID: "570"},
		{ID: "minecraft", Name: "Minecraft", Aliases: []string{"我的世界"}, Executables: []string{"minecraft.exe", "javaw.exe", "minecraft"}, SteamAppID: ""},
		{ID: "eldenring", Name: "ELDEN RING", Aliases: []string{"艾尔登法环", "Elden Ring"}, Executables: []string{"eldenring.exe", "eldenring"}, SteamAppID: "1245620"},
		{ID: "witcher3", Name: "The Witcher 3", Aliases: []string{"巫师3", "Witcher 3"}, Executables: []string{"witcher3.exe", "witcher3"}, SteamAppID: "292030"},
		{ID: "hades2", Name: "Hades II", Aliases: []string{"Hades 2", "哈迪斯2"}, Executables: []string{"hades2.exe", "hades ii.exe", "hades2"}, SteamAppID: "1145350"},
		{ID: "amongus", Name: "Among Us", Aliases: []string{"在我们之中"}, Executables: []string{"among us.exe", "amongus.exe", "among us"}, SteamAppID: "945360"},
		{ID: "apex", Name: "Apex Legends", Aliases: []string{"Apex", "艾佩克斯"}, Executables: []string{"r5apex.exe", "apex legends.exe", "r5apex"}, SteamAppID: "1172470"},
		{ID: "overwatch", Name: "Overwatch 2", Aliases: []string{"守望先锋", "OW2"}, Executables: []string{"overwatch.exe", "overwatch"}, SteamAppID: ""},
		{ID: "fortnite", Name: "Fortnite", Aliases: []string{"堡垒之夜"}, Executables: []string{"fortniteclient-win64-shipping.exe", "fortnite"}, SteamAppID: ""},
		{ID: "pubg", Name: "PUBG: BATTLEGROUNDS", Aliases: []string{"绝地求生", "PUBG"}, Executables: []string{"tslgame.exe", "pubg.exe", "tslgame"}, SteamAppID: "578080"},
		{ID: "gta5", Name: "Grand Theft Auto V", Aliases: []string{"GTA V", "GTA5", "侠盗猎车手5"}, Executables: []string{"gta5.exe", "gtav.exe", "playgtav.exe", "gta5"}, SteamAppID: "271590"},
		{ID: "rdr2", Name: "Red Dead Redemption 2", Aliases: []string{"RDR2", "荒野大镖客2"}, Executables: []string{"rdr2.exe", "rdr2"}, SteamAppID: "1174180"},
		{ID: "cyberpunk", Name: "Cyberpunk 2077", Aliases: []string{"赛博朋克2077", "赛博朋克"}, Executables: []string{"cyberpunk2077.exe", "cyberpunk2077"}, SteamAppID: "1091500"},
		{ID: "hades", Name: "Hades", Aliases: []string{"哈迪斯"}, Executables: []string{"hades.exe", "hades"}, SteamAppID: "1145360"},
		{ID: "terraria", Name: "Terraria", Aliases: []string{"泰拉瑞亚"}, Executables: []string{"terraria.exe", "terraria"}, SteamAppID: "105600"},
		{ID: "stardew", Name: "Stardew Valley", Aliases: []string{"星露谷物语"}, Executables: []string{"stardew valley.exe", "stardewvalley", "stardew valley"}, SteamAppID: "413150"},
		{ID: "osu", Name: "osu!", Aliases: []string{"osu"}, Executables: []string{"osu!.exe", "osu.exe", "osu!"}, SteamAppID: ""},
		{ID: "wuthering", Name: "鸣潮", Aliases: []string{"Wuthering Waves"}, Executables: []string{"client-win64-shipping.exe", "wuthering waves.exe", "wutheringwaves"}, SteamAppID: ""},
		{ID: "ba", Name: "Blue Archive", Aliases: []string{"碧蓝档案", "BA"}, Executables: []string{"bluearchive.exe"}, SteamAppID: ""},
		{ID: "hollowknight", Name: "Hollow Knight", Aliases: []string{"空洞骑士"}, Executables: []string{"hollow knight.exe", "hollowknight.exe", "hollow_knight"}, SteamAppID: "367520"},
		{ID: "celeste", Name: "Celeste", Aliases: []string{"蔚蓝"}, Executables: []string{"celeste.exe", "celeste"}, SteamAppID: "504230"},
		{ID: "palworld", Name: "Palworld", Aliases: []string{"幻兽帕鲁"}, Executables: []string{"palworld-win64-shipping.exe", "palworld.exe", "palworld"}, SteamAppID: "1623730"},
		{ID: "helldivers2", Name: "HELLDIVERS 2", Aliases: []string{"绝地潜兵2", "Helldivers 2"}, Executables: []string{"helldivers2.exe", "helldivers 2.exe"}, SteamAppID: "553850"},
		{ID: "bg3", Name: "Baldur's Gate 3", Aliases: []string{"博德之门3", "BG3"}, Executables: []string{"bg3.exe", "bg3_dx11.exe"}, SteamAppID: "1086940"},
		{ID: "lethal", Name: "Lethal Company", Aliases: []string{"致命公司"}, Executables: []string{"lethal company.exe", "lethalcompany.exe"}, SteamAppID: "1966720"},
		{ID: "blackmyth", Name: "黑神话：悟空", Aliases: []string{"Black Myth: Wukong", "黑神话"}, Executables: []string{"b1-win64-shipping.exe", "blackmythwukong.exe"}, SteamAppID: "2358720"},
		{ID: "naraka", Name: "永劫无间", Aliases: []string{"Naraka: Bladepoint"}, Executables: []string{"naraka bladepoint.exe", "narakabladepoint.exe"}, SteamAppID: "1203220"},
		{ID: "destiny2", Name: "Destiny 2", Aliases: []string{"命运2"}, Executables: []string{"destiny2.exe"}, SteamAppID: "1085660"},
		{ID: "warframe", Name: "Warframe", Aliases: []string{"星际战甲"}, Executables: []string{"warframe.x64.exe", "warframe.exe"}, SteamAppID: "230410"},
	}
	for i := range entries {
		if entries[i].CoverURL == "" && entries[i].SteamAppID != "" {
			entries[i].CoverURL = steamHeader(entries[i].SteamAppID)
		}
	}
	return entries
}
