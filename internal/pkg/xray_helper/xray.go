package xray_helper

import (
	"bufio"
	"github.com/WQGroup/logger"
	"github.com/allanpk716/xray_pool/internal/pkg"
	"github.com/allanpk716/xray_pool/internal/pkg/core/node"
	"github.com/allanpk716/xray_pool/internal/pkg/core/routing"
	"github.com/allanpk716/xray_pool/internal/pkg/protocols"
	"github.com/allanpk716/xray_pool/internal/pkg/settings"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type XrayHelper struct {
	index         int                    // 第几个 xray 实例
	xrayCmd       *exec.Cmd              // xray 程序的进程
	xrayPath      string                 // xray 程序的路径
	proxySettings settings.ProxySettings // 代理的配置
	route         *routing.Routing       // 路由
}

func NewXrayHelper(index int, proxySettings settings.ProxySettings, route *routing.Routing) *XrayHelper {
	return &XrayHelper{index: index, proxySettings: proxySettings, route: route}
}

// Check 检查 Xray 程序和需求的资源是否已经存在，不存在则需要提示用户去下载
func (x *XrayHelper) Check() bool {

	// 在这个目录下进行搜索是否存在 Xray 程序
	nowRootPath := pkg.GetBaseXrayFolderFPath()
	xrayExeName := pkg.GetXrayExeName()
	xrayExeFullPath := filepath.Join(nowRootPath, xrayExeName)
	if pkg.IsFile(xrayExeFullPath) == false {
		return false
	}
	// 检查 geoip.dat geosite.dat 是否存在
	geoIPResource := filepath.Join(nowRootPath, GEOIP_RESOURCE_NAME)
	geoSiteResource := filepath.Join(nowRootPath, GEOSite_RESOURCE_NAME)
	if pkg.IsFile(geoIPResource) == false || pkg.IsFile(geoSiteResource) == false {
		return false
	}

	x.xrayPath = xrayExeFullPath

	// Check 的最后就进行数据的复制
	err := pkg.CopyDir(nowRootPath, pkg.GetIndexXrayFolderFPath(x.index))
	if err != nil {
		logger.Errorf("Xray -- %2d 复制 Xray 程序失败: %s", x.index, err.Error())
		return false
	}
	return true
}

func (x *XrayHelper) Start(node *node.Node, testUrl string, testTimeOut int) bool {

	if x.run(node.Protocol) == true {
		if x.proxySettings.HttpPort == 0 {
			logger.Infof("Xray -- %2d 启动成功, 监听 socks 端口: %d, 所选节点: %s",
				x.index,
				x.proxySettings.SocksPort, node.GetName())
		} else {
			logger.Infof("Xray -- %2d 启动成功, 监听 socks/http 端口: %d/%d, 所选节点: %s",
				x.index,
				x.proxySettings.SocksPort, x.proxySettings.HttpPort, node.GetName())
		}
		result, status := x.TestNode(testUrl, x.proxySettings.SocksPort, testTimeOut)
		logger.Infof("Xray -- %2d %6s [ %s ] 延迟: %dms", x.index, status, testUrl, result)

		if result < 0 {
			x.Stop()
			logger.Infof("Xray -- %2d 当前节点: %v 访问 %v 失败, 将不再使用该节点", x.index, node.GetName(), testUrl)
			return false
		}

		return true
	} else {
		return false
	}
}

func (x *XrayHelper) run(node protocols.Protocol) bool {

	switch node.GetProtocolMode() {
	case protocols.ModeShadowSocks, protocols.ModeTrojan, protocols.ModeVMess, protocols.ModeSocks, protocols.ModeVLESS, protocols.ModeVMessAEAD:
		file := x.GenConfig(node)
		x.xrayCmd = exec.Command(x.xrayPath, "-c", file)
	default:
		logger.Errorf("Xray -- %2d 暂不支持%v协议", x.index, node.GetProtocolMode())
		return false
	}
	stdout, err := x.xrayCmd.StdoutPipe()
	if err != nil {
		logger.Errorf("Xray -- %2d 获取 xray 程序的 stdout 管道失败: %s", x.index, err.Error())
		return false
	}
	err = x.xrayCmd.Start()
	if err != nil {
		logger.Errorf("Xray -- %2d 启动 xray 程序失败: %s", x.index, err.Error())
		return false
	}
	r := bufio.NewReader(stdout)
	lines := new([]string)
	go readInfo(r, lines)
	status := make(chan struct{})
	go checkProc(x.xrayCmd, status)
	stopper := time.NewTimer(time.Millisecond * 300)
	select {
	case <-stopper.C:
		x.proxySettings.PID = x.xrayCmd.Process.Pid
		return true
	case <-status:
		logger.Error("Xray -- %2d 开启xray服务失败, 查看下面报错信息来检查出错问题", x.index)
		for _, x := range *lines {
			logger.Error(x)
		}
		return false
	}
}

// Stop 停止服务
func (x *XrayHelper) Stop() {

	if x.xrayCmd != nil {
		err := x.xrayCmd.Process.Kill()
		if err != nil {
			logger.Errorf("Xray -- %2d 停止xray服务失败: %v", x.index, err)
		}
		x.xrayCmd = nil
	} else {

		if x.proxySettings.PID != 0 {
			process, err := os.FindProcess(x.proxySettings.PID)
			if err == nil {
				err = process.Kill()
				if err != nil {
					logger.Errorf("Xray -- %2d 停止xray服务失败: %v", x.index, err)
				}
			}
			x.proxySettings.PID = 0
		}
	}
	// 日志文件过大清除
	file, err := os.Stat(x.GetLogFPath())
	if err != nil {
		logger.Errorf("Xray -- %2d os.Stat日志文件大小: %v", x.index, err.Error())
		return
	}
	if file != nil {
		fileSize := float64(file.Size()) / (1 << 20)
		if fileSize > 5 {
			err := os.Remove(x.GetLogFPath())
			if err != nil {
				logger.Errorf("Xray -- %2d 清除日志文件失败: %v", x.index, err)
			}
		}
	}
}

func readInfo(r *bufio.Reader, lines *[]string) {
	for i := 0; i < 20; i++ {
		line, _, _ := r.ReadLine()
		if len(string(line[:])) != 0 {
			*lines = append(*lines, string(line[:]))
		}
	}
}

// 检查进程状态
func checkProc(c *exec.Cmd, status chan struct{}) {
	_ = c.Wait()
	status <- struct{}{}
}

const (
	GEOIP_RESOURCE_NAME   = "geoip.dat"
	GEOSite_RESOURCE_NAME = "geosite.dat"
)