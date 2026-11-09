// Package skin 是"画面上这张皮是谁、能不能商用、要不要署名"的唯一真相。
//
// 表内容原样搬自主仓 model.Live2DModelLicenses（数值与口径都不自创）。放这里而不是散到
// 叠层/后台各自一份，是因为两处都要读：叠层要署名小字和默认参数，后台要标授权状态。
package skin

// Info 一套形象的授权与加载口径。
//   - Commercial：确认可商用（Live2D 官方示例/免费素材协议，小规模商用可）
//   - Declared：作者自己声明了口径（按条件可用，**不等于可商用**）
//   - Credit：要显示在直播画面上的署名小字——公皮作者要求"注明作者"，
//     这行字就是兑现方式；空=无需署名。
//   - Params：加载后要**每帧默认置住**的模型参数（参数名→值）。
//     典型用途：关掉贴图里烤死的水印——很多公皮内置一个"水印消除"表情
//     （本质就是把某参数置 1），我们不用每次手动切表情，加载后直接按下去。
//     在 beforeModelUpdate 里写，不会被 idle 动作/表情盖回（同口型机制）。
type Info struct {
	License    string
	Credit     string
	Params     map[string]float64
	Commercial bool
	Declared   bool
}

// Licenses 按目录名（data/host/model/<名>）登记。
// 名单外 = 来源未知，页面要标红自行核实——GitHub 上那些 model collection
// 基本都是无 License 的游戏拆包，沉默不等于默许。
var Licenses = map[string]Info{
	"ArchChan": {License: "Live2D 官方角色 Arch-chan · 免费素材协议（小规模商用可，须同意其使用条款）", Commercial: true, Declared: true},
	"Haru":     {License: "Live2D 官方示例 · 免费素材协议（小规模商用可）", Commercial: true, Declared: true},
	"Hiyori":   {License: "Live2D 官方示例 · 免费素材协议（小规模商用可）", Commercial: true, Declared: true},
	"Mao":      {License: "Live2D 官方示例 · 免费素材协议（小规模商用可）", Commercial: true, Declared: true},
	"Mark":     {License: "Live2D 官方示例 · 免费素材协议（小规模商用可）", Commercial: true, Declared: true},
	"Natori":   {License: "Live2D 官方示例 · 免费素材协议（小规模商用可）", Commercial: true, Declared: true},
	"Ren":      {License: "Live2D 官方示例 · 免费素材协议（小规模商用可）", Commercial: true, Declared: true},
	"Rice":     {License: "Live2D 官方示例 · 免费素材协议（小规模商用可）", Commercial: true, Declared: true},
	"Wanko":    {License: "Live2D 官方示例 · 免费素材协议（小规模商用可）", Commercial: true, Declared: true},
	"大肥鱼": {
		License:  "B站 @Mao绒绒家园（UID 431301506）免费公皮 · 直播须注明作者 · 禁止搬运/倒卖模型文件",
		Credit:   "模型：Mao绒绒家园",
		Params:   map[string]float64{"Paramcol_3": 1}, // 作者内置"13 水印消除"表情对应的参数
		Declared: true,
	},
}

// Default 边车的出厂形象。
const Default = "大肥鱼"

func Credit(name string) string { return Licenses[name].Credit }

func Params(name string) map[string]float64 { return Licenses[name].Params }
