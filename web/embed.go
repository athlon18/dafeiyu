// Package web 把后台页与 OBS 叠层页编进二进制。
//
// 为什么内嵌而不是让主播再开一个静态服务：边车的交付口径是"拷走就能跑"，
// 页面还要另起一个端口的话，OBS 挂的地址就得靠人记，早晚挂错。
package web

import "embed"

//go:embed overlay admin
var FS embed.FS
