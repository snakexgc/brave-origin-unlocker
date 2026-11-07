// brave-origin-unlocker
// 原理：Brave Origin 浏览器将购买/授权状态保存在 Local State (JSON) 中，
// 通过设置 brave.origin.purchase_validated = true 以及写入 skus.state["67"]
// 凭据项来解锁 Origin 功能。
//
// 用法：直接运行 exe，按控制台菜单操作：
//   1. 解锁（修改前自动备份，浏览器运行中可选择直接结束进程）
//   2. 恢复备份（列出所有备份，可自选）
//   3. 查看当前状态
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var versions = []string{"Brave-Origin", "Brave-Origin-Beta", "Brave-Origin-Nightly"}

var stdin = bufio.NewReader(os.Stdin)

func readLine() string {
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

// ---------- 路径 ----------

func localStatePath(version string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		lad := os.Getenv("LOCALAPPDATA")
		if lad == "" {
			return "", fmt.Errorf("未找到 LOCALAPPDATA 环境变量")
		}
		return filepath.Join(lad, "BraveSoftware", version, "User Data", "Local State"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "BraveSoftware", version, "Local State"), nil
	default:
		return "", fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}

// ---------- 进程检测 ----------

// 列出所有 Brave Origin 浏览器进程名。
// 注意：必须排除本工具自身（brave-origin-unlocker.exe 同样包含 "brave-origin" 子串），
// 否则会把自己误判为浏览器、甚至把自己 kill 掉。
func braveProcessNames() ([]string, error) {
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) == 0 {
			continue
		}
		name := strings.Trim(fields[0], `"`)
		lower := strings.ToLower(name)
		if strings.Contains(lower, "brave-origin") && !strings.Contains(lower, "unlocker") {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names, nil
}

func isBrowserRunning() bool {
	if runtime.GOOS == "darwin" {
		// 不用 -f 匹配命令行（会匹配到本工具路径），只按进程名匹配
		out, err := exec.Command("pgrep", "-i", "^Brave-Origin").Output()
		return err == nil && len(strings.TrimSpace(string(out))) > 0
	}
	names, err := braveProcessNames()
	if err != nil {
		return false
	}
	return len(names) > 0
}

// 结束所有 Brave Origin 浏览器进程，返回结束的进程数量
func killBrowserProcesses() (int, error) {
	switch runtime.GOOS {
	case "windows":
		names, err := braveProcessNames()
		if err != nil {
			return 0, err
		}
		if len(names) == 0 {
			return 0, nil
		}
		var lastErr error
		killed := 0
		for _, name := range names {
			if err := exec.Command("taskkill", "/F", "/T", "/IM", name).Run(); err != nil {
				lastErr = err
			} else {
				killed++
			}
		}
		return killed, lastErr
	case "darwin":
		err := exec.Command("pkill", "-i", "^Brave-Origin").Run()
		return 1, err
	}
	return 0, fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
}

// 检测浏览器运行状态：未运行则直接通过；运行中询问用户是直接结束进程还是取消
func handleRunning() bool {
	if !isBrowserRunning() {
		return true
	}
	fmt.Print("\n[!] 检测到 Brave Origin 正在运行。\n    运行中的浏览器会在退出时把 Local State 改回原样，导致操作失效。\n    是否直接结束浏览器进程并继续? (Y/n): ")
	line := strings.ToLower(readLine())
	if line == "n" || line == "no" {
		fmt.Println("已取消。请手动退出浏览器后重新运行。")
		return false
	}
	fmt.Println("    正在结束浏览器进程...")
	if _, err := killBrowserProcesses(); err != nil {
		fmt.Printf("    结束进程失败: %v，已取消。\n", err)
		return false
	}
	time.Sleep(1500 * time.Millisecond) // 等待进程完全退出、文件句柄释放
	if isBrowserRunning() {
		fmt.Println("    浏览器仍在运行，已取消。请手动退出后重试。")
		return false
	}
	fmt.Println("    浏览器已退出，继续执行。")
	return true
}

// ---------- JSON 读写 ----------

func readJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber() // 保留数字精度，避免大整数被改写
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}
	return m, nil
}

func writeJSON(path string, m map[string]any) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ---------- 备份 / 恢复 ----------

func backupFile(path string) (string, error) {
	ts := time.Now().Format("20060102-150405")
	bak := path + ".bak-" + ts
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(bak, data, 0o644); err != nil {
		return "", err
	}
	return bak, nil
}

func listBackups(path string) []string {
	matches, _ := filepath.Glob(path + ".bak-*")
	sort.Sort(sort.Reverse(sort.StringSlice(matches))) // 文件名含时间戳，倒序即最新在前
	return matches
}

// ---------- 核心操作 ----------

func applyUnlock(m map[string]any) {
	brave, _ := m["brave"].(map[string]any)
	if brave == nil {
		brave = map[string]any{}
		m["brave"] = brave
	}
	origin, _ := brave["origin"].(map[string]any)
	if origin == nil {
		origin = map[string]any{}
		brave["origin"] = origin
	}
	origin["purchase_validated"] = true

	m["skus"] = map[string]any{
		"state": map[string]any{
			"67": `{"credentials":{"items":{"6":"7"}}}`,
		},
	}
}

func isUnlocked(m map[string]any) bool {
	brave, _ := m["brave"].(map[string]any)
	if brave == nil {
		return false
	}
	origin, _ := brave["origin"].(map[string]any)
	if origin == nil {
		return false
	}
	v, _ := origin["purchase_validated"].(bool)
	return v
}

func cmdUnlock() {
	if !handleRunning() {
		return
	}
	found := false
	for _, ver := range versions {
		path, err := localStatePath(ver)
		if err != nil {
			fmt.Println("错误:", err)
			os.Exit(1)
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		bak, err := backupFile(path)
		if err != nil {
			fmt.Printf("[%s] 备份失败: %v\n", ver, err)
			continue
		}
		m, err := readJSON(path)
		if err != nil {
			fmt.Printf("[%s] 读取失败: %v\n", ver, err)
			continue
		}
		if isUnlocked(m) {
			fmt.Printf("[%s] 已是解锁状态（仍已重新备份: %s）\n", ver, filepath.Base(bak))
			found = true
			continue
		}
		applyUnlock(m)
		if err := writeJSON(path, m); err != nil {
			fmt.Printf("[%s] 写入失败: %v\n", ver, err)
			continue
		}
		fmt.Printf("[%s] 解锁成功！备份已保存: %s\n", ver, filepath.Base(bak))
		found = true
	}
	if !found {
		fmt.Println("未找到 Brave Origin 安装。")
	}
}

func cmdRestore() {
	if !handleRunning() {
		return
	}
	type item struct {
		ver string
		bak string
	}
	var items []item
	for _, ver := range versions {
		path, err := localStatePath(ver)
		if err != nil {
			fmt.Println("错误:", err)
			return
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		for _, b := range listBackups(path) { // 最新在前
			items = append(items, item{ver, b})
		}
	}
	if len(items) == 0 {
		fmt.Println("没有找到任何备份，无可恢复内容。")
		return
	}
	fmt.Println("\n可用备份:")
	for i, it := range items {
		info, _ := os.Stat(it.bak)
		fmt.Printf("  %d. [%s] %s (%s)\n", i+1, it.ver, filepath.Base(it.bak), info.ModTime().Format("2006-01-02 15:04:05"))
	}
	fmt.Printf("选择要恢复的备份编号 [1-%d, 回车=最新]: ", len(items))
	line := readLine()
	choice := 1
	if line != "" {
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(items) {
			fmt.Println("无效编号，已取消。")
			return
		}
		choice = n
	}
	sel := items[choice-1]
	data, err := os.ReadFile(sel.bak)
	if err != nil {
		fmt.Printf("[%s] 读取备份失败: %v\n", sel.ver, err)
		return
	}
	path, _ := localStatePath(sel.ver)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Printf("[%s] 恢复失败: %v\n", sel.ver, err)
		return
	}
	fmt.Printf("[%s] 已从备份恢复: %s\n", sel.ver, filepath.Base(sel.bak))
}

func cmdStatus() {
	for _, ver := range versions {
		path, err := localStatePath(ver)
		if err != nil {
			fmt.Println("错误:", err)
			os.Exit(1)
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Printf("[%s] 未安装\n", ver)
			continue
		}
		state := "未知"
		if m, err := readJSON(path); err == nil {
			if isUnlocked(m) {
				state = "已解锁"
			} else {
				state = "未解锁"
			}
		}
		baks := listBackups(path)
		fmt.Printf("[%s] %s | 备份数: %d\n", ver, state, len(baks))
		for _, b := range baks {
			info, _ := os.Stat(b)
			fmt.Printf("    - %s (%s)\n", filepath.Base(b), info.ModTime().Format("2006-01-02 15:04:05"))
		}
	}
}

func main() {
	for {
		fmt.Println("\n========================================")
		fmt.Println("       Brave Origin Unlocker")
		fmt.Println("========================================")
		fmt.Println("  1. 解锁")
		fmt.Println("  2. 恢复备份")
		fmt.Println("  3. 查看状态")
		fmt.Println("  0. 退出")
		fmt.Print("\n请选择: ")
		line := readLine()
		switch line {
		case "1":
			cmdUnlock()
		case "2":
			cmdRestore()
		case "3":
			cmdStatus()
		case "0", "q", "exit", "quit":
			return
		default:
			fmt.Println("无效选项，请输入 1 / 2 / 3 / 0。")
		}
	}
}
