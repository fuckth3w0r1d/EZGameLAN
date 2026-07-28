//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
	"golang.org/x/sys/windows"
)

const (
	windowClass = "EZGameLANClientWindow"
	windowTitle = "EZGameLAN"

	wmCreate  = 0x0001
	wmDestroy = 0x0002
	wmClose   = 0x0010
	wmCommand = 0x0111
	wmSetFont = 0x0030
	wmAppDone = 0x8001
	wmAppTray = 0x8002

	wmLButtonDoubleClick = 0x0203
	wmRButtonUp          = 0x0205

	wsChild       = 0x40000000
	wsVisible     = 0x10000000
	wsBorder      = 0x00800000
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	esAutoHScroll = 0x0080
	esPassword    = 0x0020

	swHide         = 0
	swShow         = 5
	swRestore      = 9
	colorWindow    = 5
	idcArrow       = 32512
	idiApplication = 32512
	defaultGUIFont = 17
	emSetLimitText = 0x00C5

	idJoin       = 1001
	idConnect    = 1002
	idDisconnect = 1003
	idShow       = 1004
	idExit       = 1005

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString       = 0x00000000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	gdi32   = windows.NewLazySystemDLL("gdi32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procRegisterClassEx = user32.NewProc("RegisterClassExW")
	procCreateWindowEx  = user32.NewProc("CreateWindowExW")
	procDefWindowProc   = user32.NewProc("DefWindowProcW")
	procShowWindow      = user32.NewProc("ShowWindow")
	procUpdateWindow    = user32.NewProc("UpdateWindow")
	procGetMessage      = user32.NewProc("GetMessageW")
	procTranslateMsg    = user32.NewProc("TranslateMessage")
	procDispatchMsg     = user32.NewProc("DispatchMessageW")
	procPostQuitMessage = user32.NewProc("PostQuitMessage")
	procPostMessage     = user32.NewProc("PostMessageW")
	procLoadCursor      = user32.NewProc("LoadCursorW")
	procLoadIcon        = user32.NewProc("LoadIconW")
	procSetWindowText   = user32.NewProc("SetWindowTextW")
	procGetWindowText   = user32.NewProc("GetWindowTextW")
	procGetTextLength   = user32.NewProc("GetWindowTextLengthW")
	procEnableWindow    = user32.NewProc("EnableWindow")
	procSendMessage     = user32.NewProc("SendMessageW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
	procCreatePopupMenu = user32.NewProc("CreatePopupMenu")
	procAppendMenu      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu  = user32.NewProc("TrackPopupMenu")
	procDestroyMenu     = user32.NewProc("DestroyMenu")
	procGetCursorPos    = user32.NewProc("GetCursorPos")
	procGetStockObject  = gdi32.NewProc("GetStockObject")
	procShellNotifyIcon = shell32.NewProc("Shell_NotifyIconW")

	ui clientUI
)

type clientUI struct {
	hwnd             uintptr
	urlEdit          uintptr
	tokenEdit        uintptr
	joinButton       uintptr
	connectButton    uintptr
	disconnectButton uintptr
	statusLabel      uintptr

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	busy     bool
	response ipc.Response
	err      error

	initialURL   string
	initialToken string
	trayAdded    bool
}

type point struct {
	X int32
	Y int32
}

type message struct {
	HWnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Point    point
	LPrivate uint32
}

type windowClassEx struct {
	Size       uint32
	Style      uint32
	WindowProc uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

type notifyIconData struct {
	Size            uint32
	Window          uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Timeout         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            windows.GUID
	BalloonIcon     uintptr
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ui.ctx, ui.cancel = context.WithCancel(context.Background())
	defer ui.cancel()
	if len(os.Args) == 2 {
		if invitation, err := join.ParseLink(os.Args[1]); err == nil {
			ui.initialURL = invitation.URL
			ui.initialToken = invitation.Token
		}
	}

	className := utf16(windowClass)
	title := utf16(windowTitle)
	cursor, _, _ := procLoadCursor.Call(0, idcArrow)
	class := windowClassEx{
		Size:       uint32(unsafe.Sizeof(windowClassEx{})),
		WindowProc: windows.NewCallback(windowProc),
		Cursor:     cursor,
		Background: colorWindow + 1,
		ClassName:  className,
	}
	atom, _, callErr := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return fmt.Errorf("E_INSTALL_UI: 注册窗口失败: %w", callErr)
	}
	hwnd, _, callErr := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsCaption|wsSysMenu|wsMinimizeBox,
		0x80000000,
		0x80000000,
		620,
		330,
		0,
		0,
		0,
		0,
	)
	if hwnd == 0 {
		return fmt.Errorf("E_INSTALL_UI: 创建窗口失败: %w", callErr)
	}
	ui.hwnd = hwnd
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	ui.start(ipc.Request{Version: ipc.Version, Action: ipc.ActionStatus}, "正在读取状态…")

	var msg message
	for {
		result, _, err := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			return fmt.Errorf("E_INSTALL_UI: 读取窗口消息失败: %w", err)
		}
		if result == 0 {
			break
		}
		procTranslateMsg.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMsg.Call(uintptr(unsafe.Pointer(&msg)))
	}
	ui.wg.Wait()
	return nil
}

func windowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCreate:
		createControls(hwnd)
		ui.trayAdded = updateTrayIcon(hwnd, nimAdd, "EZGameLAN")
		return 0
	case wmCommand:
		switch uint16(wParam & 0xffff) {
		case idJoin:
			enrollURL := getText(ui.urlEdit, 2048)
			token := getText(ui.tokenEdit, 256)
			setText(ui.tokenEdit, "")
			ui.start(ipc.Request{
				Version: ipc.Version,
				Action:  ipc.ActionJoin,
				URL:     enrollURL,
				Token:   token,
			}, "正在领取配置并连接…")
		case idConnect:
			ui.start(
				ipc.Request{Version: ipc.Version, Action: ipc.ActionConnect},
				"正在连接…",
			)
		case idDisconnect:
			ui.start(
				ipc.Request{Version: ipc.Version, Action: ipc.ActionDisconnect},
				"正在断开…",
			)
		case idShow:
			showMainWindow(hwnd)
		case idExit:
			procDestroyWindow.Call(hwnd)
		}
		return 0
	case wmAppDone:
		ui.finish()
		return 0
	case wmAppTray:
		switch uint32(lParam) {
		case wmLButtonDoubleClick:
			showMainWindow(hwnd)
		case wmRButtonUp:
			showTrayMenu(hwnd)
		}
		return 0
	case wmClose:
		if ui.trayAdded {
			procShowWindow.Call(hwnd, swHide)
			return 0
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		if ui.trayAdded {
			updateTrayIcon(hwnd, nimDelete, "")
			ui.trayAdded = false
		}
		ui.cancel()
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProc.Call(hwnd, uintptr(msg), wParam, lParam)
	return result
}

func createControls(hwnd uintptr) {
	font, _, _ := procGetStockObject.Call(defaultGUIFont)
	createLabel(hwnd, "服务器 URL", 24, 22, 120, 22, font)
	ui.urlEdit = createControl(
		"EDIT",
		ui.initialURL,
		wsChild|wsVisible|wsBorder|esAutoHScroll,
		24, 46, 560, 26,
		hwnd, 0, font,
	)
	procSendMessage.Call(ui.urlEdit, emSetLimitText, 2048, 0)

	createLabel(hwnd, "Token", 24, 84, 120, 22, font)
	ui.tokenEdit = createControl(
		"EDIT",
		ui.initialToken,
		wsChild|wsVisible|wsBorder|esAutoHScroll|esPassword,
		24, 108, 560, 26,
		hwnd, 0, font,
	)
	procSendMessage.Call(ui.tokenEdit, emSetLimitText, 256, 0)

	ui.joinButton = createControl(
		"BUTTON", "加入并连接", wsChild|wsVisible,
		24, 154, 170, 34, hwnd, idJoin, font,
	)
	ui.connectButton = createControl(
		"BUTTON", "连接", wsChild|wsVisible,
		214, 154, 170, 34, hwnd, idConnect, font,
	)
	ui.disconnectButton = createControl(
		"BUTTON", "断开", wsChild|wsVisible,
		404, 154, 180, 34, hwnd, idDisconnect, font,
	)
	ui.statusLabel = createControl(
		"STATIC", "正在启动…", wsChild|wsVisible,
		24, 210, 560, 60, hwnd, 0, font,
	)
}

func createLabel(
	parent uintptr,
	text string,
	x, y, width, height int,
	font uintptr,
) uintptr {
	return createControl("STATIC", text, wsChild|wsVisible, x, y, width, height, parent, 0, font)
}

func createControl(
	className, text string,
	style uintptr,
	x, y, width, height int,
	parent, id, font uintptr,
) uintptr {
	classPtr := utf16(className)
	textPtr := utf16(text)
	handle, _, _ := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(textPtr)),
		style,
		uintptr(x),
		uintptr(y),
		uintptr(width),
		uintptr(height),
		parent,
		id,
		0,
		0,
	)
	if handle != 0 && font != 0 {
		procSendMessage.Call(handle, wmSetFont, font, 1)
	}
	return handle
}

func (u *clientUI) start(request ipc.Request, status string) {
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return
	}
	u.busy = true
	u.mu.Unlock()
	u.setButtons(false)
	setText(u.statusLabel, status)

	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		ctx, cancel := context.WithTimeout(u.ctx, 40*time.Second)
		defer cancel()
		response, err := ipc.Call(ctx, request)
		u.mu.Lock()
		u.response = response
		u.err = err
		u.mu.Unlock()
		procPostMessage.Call(u.hwnd, wmAppDone, 0, 0)
	}()
}

func (u *clientUI) finish() {
	u.mu.Lock()
	response := u.response
	err := u.err
	u.busy = false
	u.mu.Unlock()
	u.setButtons(true)
	if err != nil {
		status := "后台服务未响应，请重新启动或重新安装 EZGameLAN。"
		setText(u.statusLabel, status)
		updateTrayIcon(u.hwnd, nimModify, status)
		return
	}
	if !response.OK {
		setText(u.statusLabel, response.Message)
		updateTrayIcon(u.hwnd, nimModify, response.Message)
		return
	}
	status := statusText(response.Status)
	setText(u.statusLabel, status)
	updateTrayIcon(u.hwnd, nimModify, status)
}

func (u *clientUI) setButtons(enabled bool) {
	value := uintptr(0)
	if enabled {
		value = 1
	}
	for _, handle := range []uintptr{u.joinButton, u.connectButton, u.disconnectButton} {
		if handle != 0 {
			procEnableWindow.Call(handle, value)
		}
	}
}

func statusText(status ipc.Status) string {
	switch status.State {
	case ipc.StateNotConfigured:
		return "尚未加入：请填写 URL 和 Token。"
	case ipc.StateDisconnected:
		return "已断开。"
	case ipc.StateConnecting:
		return "正在连接虚拟局域网…"
	case ipc.StateConnected:
		if status.VirtualIP != "" {
			return "已连接，虚拟 IP：" + status.VirtualIP
		}
		return "已连接。"
	case ipc.StateStopping:
		return "正在断开…"
	case ipc.StateError:
		return "连接出现问题，请点击“连接”重试。"
	default:
		return "状态未知。"
	}
}

func getText(handle uintptr, limit int) string {
	length, _, _ := procGetTextLength.Call(handle)
	if int(length) > limit {
		length = uintptr(limit)
	}
	buffer := make([]uint16, int(length)+1)
	procGetWindowText.Call(handle, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return windows.UTF16ToString(buffer)
}

func setText(handle uintptr, text string) {
	if handle == 0 {
		return
	}
	value := utf16(text)
	procSetWindowText.Call(handle, uintptr(unsafe.Pointer(value)))
}

func showMainWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, swRestore)
	procSetForeground.Call(hwnd)
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	procAppendMenu.Call(menu, mfString, idShow, uintptr(unsafe.Pointer(utf16("打开"))))
	procAppendMenu.Call(menu, mfString, idExit, uintptr(unsafe.Pointer(utf16("退出"))))
	var cursor point
	if result, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor))); result == 0 {
		return
	}
	procSetForeground.Call(hwnd)
	command, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmReturnCmd,
		uintptr(cursor.X),
		uintptr(cursor.Y),
		0,
		hwnd,
		0,
	)
	switch command {
	case idShow:
		showMainWindow(hwnd)
	case idExit:
		procDestroyWindow.Call(hwnd)
	}
}

func updateTrayIcon(hwnd uintptr, operation uint32, tip string) bool {
	icon, _, _ := procLoadIcon.Call(0, idiApplication)
	data := notifyIconData{
		Size:            uint32(unsafe.Sizeof(notifyIconData{})),
		Window:          hwnd,
		ID:              1,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmAppTray,
		Icon:            icon,
	}
	fillUTF16(data.Tip[:], tip)
	result, _, _ := procShellNotifyIcon.Call(
		uintptr(operation),
		uintptr(unsafe.Pointer(&data)),
	)
	return result != 0
}

func fillUTF16(destination []uint16, text string) {
	encoded := windows.StringToUTF16(strings.ReplaceAll(text, "\x00", ""))
	if len(encoded) > len(destination) {
		encoded = encoded[:len(destination)]
		encoded[len(encoded)-1] = 0
	}
	copy(destination, encoded)
}

func utf16(text string) *uint16 {
	value, _ := windows.UTF16PtrFromString(strings.ReplaceAll(text, "\x00", ""))
	return value
}
