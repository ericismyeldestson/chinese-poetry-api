package classifier

import (
	"strings"
	"unicode/utf8"
)

// 诗词体裁相关常量。
const (
	// 大类
	// CategoryPoetry describes the literary form family, not a dynasty. Song,
	// Wei-Jin, and other poems may share the same regulated-verse forms, so using
	// "唐诗" here incorrectly labels non-Tang works in the public API.
	CategoryPoetry = "诗"
	CategoryCi     = "宋词"
	CategoryOther  = "其他"

	// 具体体裁
	TypeWuyanJueju = "五言绝句"
	TypeQiyanJueju = "七言绝句"
	TypeWuyanLvshi = "五言律诗"
	TypeQiyanLvshi = "七言律诗"
	TypeCi         = "宋词"
	TypeOther      = "其他"

	// 格律结构约束
	JuejuLines = 4
	LvshiLines = 8
	WuyanChars = 5
	QiyanChars = 7
)

// PoetryTypeInfo 描述一次体裁判定的结果。
type PoetryTypeInfo struct {
	TypeName     string
	Category     string
	Lines        *int
	CharsPerLine *int
}

// ClassifyPoetryType 仅依据结构判定诗词体裁。
func ClassifyPoetryType(paragraphs []string, rhythmic string) PoetryTypeInfo {
	return ClassifyPoetryTypeWithDataset(paragraphs, rhythmic, "", "")
}

// ClassifyPoetryTypeWithDataset 结合数据集来源与文本结构判定诗词体裁。
// 判定优先级：
//  1. 按数据集直接映射（诗经、楚辞、论语、孟子、元曲等）
//  2. 有词牌名（rhythmic）则判为词（宋词等）
//  3. 按标题识别乐府诗
//  4. 按结构分析判定（诗）
func ClassifyPoetryTypeWithDataset(paragraphs []string, rhythmic string, datasetKey string, title string) PoetryTypeInfo {
	// 优先级 1：按数据集 key 直接映射
	if typeInfo, ok := getTypeFromDataset(datasetKey); ok {
		return typeInfo
	}

	// 优先级 2：带词牌名的判为词
	if rhythmic != "" {
		return PoetryTypeInfo{
			TypeName: TypeCi,
			Category: CategoryCi,
		}
	}

	// 优先级 3：按标题识别乐府诗
	if title != "" && isYuefuPoem(title) {
		return PoetryTypeInfo{
			TypeName: "乐府诗",
			Category: CategoryPoetry,
		}
	}

	// 优先级 4：按结构判定诗歌体裁
	if len(paragraphs) == 0 {
		return PoetryTypeInfo{
			TypeName: TypeOther,
			Category: CategoryOther,
		}
	}

	// 拆分被合并的诗句，如 "江南有美人，别后长相忆。" → ["江南有美人", "别后长相忆"]
	expandedLines := expandParagraphs(paragraphs)

	// 拆分后若无有效诗句则归入其他
	if len(expandedLines) == 0 {
		return PoetryTypeInfo{
			TypeName: TypeOther,
			Category: CategoryOther,
		}
	}

	// 统计句数与每句字数
	lineCount := len(expandedLines)
	charCounts := make([]int, lineCount)

	for i, line := range expandedLines {
		// 去掉标点后再计字数
		cleaned := removePunctuation(line)
		charCounts[i] = utf8.RuneCountInString(cleaned)
	}

	// 各句字数不一致则视为不规则结构
	if !isUniform(charCounts) {
		return PoetryTypeInfo{
			TypeName: TypeOther,
			Category: CategoryOther,
		}
	}

	charsPerLine := charCounts[0]

	// 依据句数与每句字数判定具体体裁
	typeName, category := classifyByStructure(lineCount, charsPerLine)

	return PoetryTypeInfo{
		TypeName:     typeName,
		Category:     category,
		Lines:        &lineCount,
		CharsPerLine: &charsPerLine,
	}
}

// getTypeFromDataset 按数据集 key 返回对应的体裁信息。
// 存在直接映射时返回 (typeInfo, true)，否则返回 (零值, false)。
func getTypeFromDataset(datasetKey string) (PoetryTypeInfo, bool) {
	// 数据集 key 到体裁的映射表
	datasetTypeMap := map[string]PoetryTypeInfo{
		"shijing": {
			TypeName: "诗经",
			Category: "诗经",
		},
		"chuci": {
			TypeName: "楚辞",
			Category: "楚辞",
		},
		"lunyu": {
			TypeName: "论语",
			Category: "论语",
		},
		"mengzi": {
			TypeName: "四书五经",
			Category: "四书五经",
		},
		"yuanqu": {
			TypeName: "元曲",
			Category: "曲",
		},
		"wudai-huajianji": {
			TypeName: "五代词",
			Category: "词",
		},
		"wudai-nantang": {
			TypeName: "五代词",
			Category: "词",
		},
		"nalanxingde": {
			TypeName: "宋词", // 纳兰性德是清代，但词的形式与宋词相同
			Category: "宋词",
		},
		"caocao": {
			TypeName: "乐府诗",
			Category: CategoryPoetry,
		},
	}

	if typeInfo, ok := datasetTypeMap[datasetKey]; ok {
		return typeInfo, true
	}

	return PoetryTypeInfo{}, false
}

// classifyByStructure 依据句数与每句字数判定绝句、律诗等体裁。
func classifyByStructure(lines, chars int) (typeName, category string) {
	switch {
	case lines == JuejuLines && chars == WuyanChars:
		return TypeWuyanJueju, CategoryPoetry
	case lines == JuejuLines && chars == QiyanChars:
		return TypeQiyanJueju, CategoryPoetry
	case lines == LvshiLines && chars == WuyanChars:
		return TypeWuyanLvshi, CategoryPoetry
	case lines == LvshiLines && chars == QiyanChars:
		return TypeQiyanLvshi, CategoryPoetry
	default:
		return TypeOther, CategoryOther
	}
}

// isUniform 判断切片中的整数是否全部相等。
func isUniform(nums []int) bool {
	if len(nums) == 0 {
		return true
	}
	first := nums[0]
	for _, n := range nums[1:] {
		if n != first {
			return false
		}
	}
	return true
}

// expandParagraphs 按句末标点把段落拆分为单句。
func expandParagraphs(paragraphs []string) []string {
	var result []string

	for _, para := range paragraphs {
		lines := splitBySentence(para)
		result = append(result, lines...)
	}

	return result
}

// splitBySentence 按句末标点（。！？；，）切分文本，并丢弃空白片段。
func splitBySentence(text string) []string {
	// 先把句末标点统一替换成换行符
	delimiters := []string{"。", "！", "？", "；", "，"}
	for _, delim := range delimiters {
		text = strings.ReplaceAll(text, delim, "\n")
	}

	// 再按换行切分并过滤空串
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}

	return result
}

// removePunctuation 去除文本中的所有标点。
func removePunctuation(text string) string {
	// 常见中英文标点
	punctuation := `，。！？；：""''（）《》【】、·—…,.!?;:'"()[]{}/-`

	// 用 strings.Map 单趟过滤，避免多次分配
	result := strings.Map(func(r rune) rune {
		if strings.ContainsRune(punctuation, r) {
			return -1 // 丢弃该字符
		}
		return r
	}, text)

	return strings.TrimSpace(result)
}

// isYuefuPoem 依据标题判断是否为乐府诗。
// 注意：这里只维护一份简体的匹配词表，匹配前先把传入标题转为简体，
// 从而免去同时维护简繁两套词表，也避免两者不一致。
func isYuefuPoem(title string) bool {
	// 统一转简体后再匹配，转换失败则退回原标题
	simplifiedTitle, err := ToSimplified(title)
	if err != nil {
		simplifiedTitle = title
	}

	// 常见乐府诗题（仅简体）
	yuefuTitles := []string{
		// 边塞乐府
		"凉州词", "出塞", "从军行", "塞下曲", "塞上曲",
		"关山月", "渡荆门", "渡远荆门外",

		// 送别乐府
		"送友人", "送孟浩然", "送元二使安西",
		"送友人入蜀", "宣州送裴坡判", "宣州送裴坡判官归京",

		// 抒情乐府
		"将进酒", "行路难", "长相思", "春思", "秋思",
		"子夜吴歌", "清平调",

		// 山水游历
		"蜀道难", "梦游天姥", "侠客行",
		"登金陵凤凰台", "黄鹤楼",
		"宣州谢脁楼", "宣城见杜鹃花",
		"宣州谢脁楼饯别校书叔云",
		"渡浙江问舟中人",

		// 白居易乐府
		"琵琶行", "长恨歌", "卖炭翁", "观刈麦",
		"新丰折臂翁", "上阳白发人", "井底引银瓶",
		"杜陵叟", "缭绫",

		// 杜甫乐府
		"兵车行", "丽人行", "哀江头", "哀王孙",
		"新安吏", "石壕吏", "潼关吏",
		"新婚别", "垂老别", "无家别",

		// 王维乐府
		"老将行", "桃源行", "洛阳女儿行",

		// 高适乐府
		"燕歌行", "别董大", "营州歌",

		// 岑参乐府
		"白雪歌", "走马川", "轮台歌",

		// 王昌龄乐府
		"芙蓉楼", "闺怨",

		// 刘禹锡乐府
		"竹枝词", "杨柳枝", "浪淘沙", "乌衣巷",
		"石头城", "西塞山怀古",

		// 韩愈乐府
		"山石", "谒衡岳庙", "八月十五夜赠张功曹",

		// 柳宗元乐府
		"渔翁", "江雪",

		// 孟郊乐府
		"游子吟", "秋怀", "烈女操",

		// 元稹乐府
		"遣悲怀", "离思", "行宫",

		// 李贺乐府
		"雁门太守行", "金铜仙人辞汉歌", "苏小小墓",
		"梦天", "李凭箜篌引",

		// 其他常见乐府题
		"古风", "古意", "拟古", "采莲曲", "江南曲",
		"白头吟", "怨歌行", "短歌行", "长歌行",
		"陇西行", "陌上桑", "木兰诗",
		"孔雀东南飞", "悲愤诗",

		// 汉魏六朝乐府
		"饮马长城窟行", "十五从军征", "上邪",
		"有所思", "上山采蘼芜", "江南",
	}

	// 先按标题词表匹配
	for _, yuefuTitle := range yuefuTitles {
		if strings.Contains(simplifiedTitle, yuefuTitle) {
			return true
		}
	}

	// 再按常见后缀匹配：曲辞、歌辞、歌行、乐府、新乐府都是典型的乐府标志
	yuefuPatterns := []string{
		"曲辞", "歌辞", "歌行", "乐府", "新乐府",
	}

	for _, pattern := range yuefuPatterns {
		if strings.Contains(simplifiedTitle, pattern) {
			return true
		}
	}

	return false
}
