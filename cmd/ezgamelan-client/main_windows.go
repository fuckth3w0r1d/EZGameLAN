//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
	"github.com/r3t2/ezgamelan/internal/logging"
	"golang.org/x/sys/windows"
)

const (
	windowClass = "EZGameLANClientWindow"
	windowTitle = "EZGameLAN"

	wmCreate    = 0x0001
	wmDestroy   = 0x0002
	wmClose     = 0x0010
	wmCommand   = 0x0111
	wmTimer     = 0x0113
	wmSetFont   = 0x0030
	wmAppDone   = 0x8001
	wmAppTray   = 0x8002
	wmAppStatus = 0x8003

	wmLButtonDoubleClick = 0x0203
	wmRButtonUp          = 0x0205

	wsChild       = 0x40000000
	wsVisible     = 0x10000000
	wsBorder      = 0x00800000
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	wsVScroll     = 0x00200000
	wsTabStop     = 0x00010000
	esAutoHScroll = 0x0080
	lbsNotify     = 0x0001
	lbsNoIntegral = 0x0100

	swHide         = 0
	swShow         = 5
	swRestore      = 9
	colorWindow    = 5
	idcArrow       = 32512
	idiApplication = 32512
	defaultGUIFont = 17
	emSetLimitText = 0x00C5
	emSetCueBanner = 0x1501
	lbAddString    = 0x0180
	lbResetContent = 0x0184
	lbSetCurSel    = 0x0186
	lbGetCurSel    = 0x0188
	lbnSelChange   = 1
	lbnDoubleClick = 2

	idCredentialList = 1000
	idJoin           = 1001
	idConnect        = 1002
	idDisconnect     = 1003
	idExport         = 1004
	idShow           = 1005
	idExit           = 1006
	idDelete         = 1007
	idStatusTimer    = 2001

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString       = 0x00000000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	ofnOverwritePrompt = 0x00000002
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800

	mbYesNo       = 0x00000004
	mbIconWarning = 0x00000030
	mbDefButton2  = 0x00000100
	idYes         = 6
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")

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
	procMessageBox      = user32.NewProc("MessageBoxW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
	procCreatePopupMenu = user32.NewProc("CreatePopupMenu")
	procAppendMenu      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu  = user32.NewProc("TrackPopupMenu")
	procDestroyMenu     = user32.NewProc("DestroyMenu")
	procGetCursorPos    = user32.NewProc("GetCursorPos")
	procSetTimer        = user32.NewProc("SetTimer")
	procKillTimer       = user32.NewProc("KillTimer")
	procGetStockObject  = gdi32.NewProc("GetStockObject")
	procShellNotifyIcon = shell32.NewProc("Shell_NotifyIconW")
	procGetSaveFileName = comdlg32.NewProc("GetSaveFileNameW")

	ui clientUI
)

type clientUI struct {
	hwnd             uintptr
	credentialList   uintptr
	joinEdit         uintptr
	joinButton       uintptr
	connectButton    uintptr
	disconnectButton uintptr
	deleteButton     uintptr
	exportButton     uintptr
	statusLabel      uintptr

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu           sync.Mutex
	busy         bool
	response     ipc.Response
	err          error
	action       ipc.Action
	targetID     string
	polling      bool
	pollResponse ipc.Response
	pollErr      error

	initialInput string
	credentials  []ipc.Credential
	lastStatus   ipc.Status
	statusShown  bool
	trayAdded    bool
	logFile      *logging.RotatingFile
	logger       *slog.Logger
	logInitErr   error
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

type openFileName struct {
	Size             uint32
	Owner            uintptr
	Instance         uintptr
	Filter           *uint16
	CustomFilter     *uint16
	MaxCustomFilter  uint32
	FilterIndex      uint32
	File             *uint16
	MaxFile          uint32
	FileTitle        *uint16
	MaxFileTitle     uint32
	InitialDirectory *uint16
	Title            *uint16
	Flags            uint32
	FileOffset       uint16
	FileExtension    uint16
	DefaultExtension *uint16
	CustomData       uintptr
	Hook             uintptr
	TemplateName     *uint16
	Reserved         uintptr
	ReservedValue    uint32
	FlagsEx          uint32
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// Win32 windows and their message queues belong to the OS thread that
	// creates them. Keep creation and the entire message loop on that thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	initializeClientLogging()
	if ui.logFile != nil {
		defer ui.logFile.Close()
	}
	ui.logger.Info("EZGameLAN client UI starting")

	ui.ctx, ui.cancel = context.WithCancel(context.Background())
	defer ui.cancel()
	if len(os.Args) == 2 {
		if _, err := join.ParseInput(os.Args[1]); err == nil {
			ui.initialInput = os.Args[1]
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
		680,
		520,
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
	ui.start(
		ipc.Request{Version: ipc.Version, Action: ipc.ActionStatus},
		"正在读取状态…",
	)

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
	ui.logger.Info("EZGameLAN client UI stopped")
	return nil
}

func windowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCreate:
		createControls(hwnd)
		ui.trayAdded = updateTrayIcon(hwnd, nimAdd, "EZGameLAN")
		procSetTimer.Call(hwnd, idStatusTimer, 2000, 0)
		return 0
	case wmCommand:
		switch uint16(wParam & 0xffff) {
		case idCredentialList:
			switch uint16((wParam >> 16) & 0xffff) {
			case lbnSelChange:
				ui.setButtonsForStatus(ui.lastStatus)
			case lbnDoubleClick:
				ui.connectSelected()
			}
		case idJoin:
			raw := getText(ui.joinEdit, 8<<10)
			setText(ui.joinEdit, "")
			invitation, err := join.ParseInput(raw)
			if err != nil {
				ui.logger.Warn("join input rejected", "code", "E_JOIN_LINK")
				status := "加入信息格式错误。\r\n请粘贴完整加入链接，或 URL 和 Token。"
				setText(ui.statusLabel, status)
				updateTrayIcon(ui.hwnd, nimModify, "EZGameLAN：加入信息格式错误")
				return 0
			}
			ui.start(ipc.Request{
				Version: ipc.Version,
				Action:  ipc.ActionJoin,
				URL:     invitation.URL,
				Token:   invitation.Token,
			}, "正在领取配置并连接…")
		case idConnect:
			ui.connectSelected()
		case idDisconnect:
			ui.start(
				ipc.Request{Version: ipc.Version, Action: ipc.ActionDisconnect},
				"正在断开…",
			)
		case idDelete:
			ui.deleteSelected()
		case idExport:
			ui.start(
				ipc.Request{Version: ipc.Version, Action: ipc.ActionDiagnostics},
				"正在收集最近的诊断日志…",
			)
		case idShow:
			showMainWindow(hwnd)
		case idExit:
			procDestroyWindow.Call(hwnd)
		}
		return 0
	case wmTimer:
		if wParam == idStatusTimer {
			ui.pollStatus()
		}
		return 0
	case wmAppDone:
		ui.finish()
		return 0
	case wmAppStatus:
		ui.finishStatusPoll()
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
		procKillTimer.Call(hwnd, idStatusTimer)
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
	createLabel(hwnd, "已保存的局域网", 24, 18, 180, 22, font)
	ui.credentialList = createControl(
		"LISTBOX", "",
		wsChild|wsVisible|wsBorder|wsVScroll|wsTabStop|lbsNotify|lbsNoIntegral,
		24, 42, 620, 118,
		hwnd, idCredentialList, font,
	)
	ui.connectButton = createControl(
		"BUTTON", "连接所选", wsChild|wsVisible|wsTabStop,
		24, 174, 145, 34, hwnd, idConnect, font,
	)
	ui.disconnectButton = createControl(
		"BUTTON", "断开", wsChild|wsVisible|wsTabStop,
		183, 174, 145, 34, hwnd, idDisconnect, font,
	)
	ui.deleteButton = createControl(
		"BUTTON", "删除凭证", wsChild|wsVisible|wsTabStop,
		342, 174, 145, 34, hwnd, idDelete, font,
	)
	ui.exportButton = createControl(
		"BUTTON", "导出日志", wsChild|wsVisible|wsTabStop,
		501, 174, 143, 34, hwnd, idExport, font,
	)
	ui.statusLabel = createControl(
		"STATIC", "正在启动后台服务…", wsChild|wsVisible,
		24, 224, 620, 88, hwnd, 0, font,
	)

	createLabel(hwnd, "添加局域网", 24, 326, 180, 22, font)
	ui.joinEdit = createControl(
		"EDIT",
		ui.initialInput,
		wsChild|wsVisible|wsBorder|wsTabStop|esAutoHScroll,
		24, 350, 620, 28,
		hwnd, 0, font,
	)
	procSendMessage.Call(ui.joinEdit, emSetLimitText, 8<<10, 0)
	procSendMessage.Call(
		ui.joinEdit,
		emSetCueBanner,
		1,
		uintptr(unsafe.Pointer(utf16("粘贴 ezgamelan:// 链接，或 URL + Token"))),
	)
	createLabel(
		hwnd,
		"支持完整加入链接，也支持将 URL 和 Token 用空格或换行分隔后一起粘贴。",
		24, 384, 620, 22, font,
	)

	ui.joinButton = createControl(
		"BUTTON", "添加并连接", wsChild|wsVisible|wsTabStop,
		24, 414, 145, 34, hwnd, idJoin, font,
	)
	ui.lastStatus = ipc.Status{State: ipc.StateNotConfigured}
	ui.setButtonsForStatus(ui.lastStatus)
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

func (u *clientUI) connectSelected() {
	credential, ok := u.selectedCredential()
	if !ok {
		u.showSelectionRequired()
		return
	}
	u.start(ipc.Request{
		Version:      ipc.Version,
		Action:       ipc.ActionConnect,
		CredentialID: credential.ID,
	}, "正在连接所选局域网…")
}

func (u *clientUI) deleteSelected() {
	credential, ok := u.selectedCredential()
	if !ok {
		u.showSelectionRequired()
		return
	}
	message := "删除本机凭证“" + credentialDisplay(credential) + "”？\r\n\r\n" +
		"删除后需要新的邀请链接才能再次加入。"
	result, _, _ := procMessageBox.Call(
		u.hwnd,
		uintptr(unsafe.Pointer(utf16(message))),
		uintptr(unsafe.Pointer(utf16("删除凭证"))),
		mbYesNo|mbIconWarning|mbDefButton2,
	)
	if result != idYes {
		return
	}
	u.start(ipc.Request{
		Version:      ipc.Version,
		Action:       ipc.ActionDeleteCredential,
		CredentialID: credential.ID,
	}, "正在删除本机凭证…")
}

func (u *clientUI) selectedCredential() (ipc.Credential, bool) {
	if u.credentialList == 0 {
		return ipc.Credential{}, false
	}
	selection, _, _ := procSendMessage.Call(u.credentialList, lbGetCurSel, 0, 0)
	if int32(selection) < 0 || int(selection) >= len(u.credentials) {
		return ipc.Credential{}, false
	}
	return u.credentials[int(selection)], true
}

func (u *clientUI) showSelectionRequired() {
	status := "请先从列表中选择一个局域网。"
	setText(u.statusLabel, status)
	updateTrayIcon(u.hwnd, nimModify, status)
}

func (u *clientUI) start(request ipc.Request, status string) {
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return
	}
	u.busy = true
	u.action = request.Action
	u.targetID = request.CredentialID
	u.mu.Unlock()
	if request.Action != ipc.ActionStatus {
		u.logger.Info("UI operation started", "action", request.Action)
	}
	u.setButtons(false)
	if status != "" {
		setText(u.statusLabel, status)
	}

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

func (u *clientUI) pollStatus() {
	u.mu.Lock()
	if u.polling || u.busy {
		u.mu.Unlock()
		return
	}
	u.polling = true
	u.mu.Unlock()

	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		ctx, cancel := context.WithTimeout(u.ctx, 5*time.Second)
		defer cancel()
		response, err := ipc.Call(ctx, ipc.Request{
			Version: ipc.Version,
			Action:  ipc.ActionStatus,
		})
		u.mu.Lock()
		u.pollResponse = response
		u.pollErr = err
		u.mu.Unlock()
		procPostMessage.Call(u.hwnd, wmAppStatus, 0, 0)
	}()
}

func (u *clientUI) finishStatusPoll() {
	u.mu.Lock()
	response := u.pollResponse
	err := u.pollErr
	busy := u.busy
	u.polling = false
	u.mu.Unlock()
	if busy {
		return
	}
	if err != nil {
		u.logger.Warn("status refresh failed", "error", err)
		u.statusShown = false
		status := "后台服务未响应，请在 Windows“服务”中重启 EZGameLAN。"
		setText(u.statusLabel, status)
		updateTrayIcon(u.hwnd, nimModify, status)
		return
	}
	if !response.OK {
		u.logger.Warn(
			"status refresh rejected",
			"code", response.Code,
		)
		u.statusShown = false
		setText(u.statusLabel, response.Message)
		updateTrayIcon(u.hwnd, nimModify, response.Message)
		return
	}
	u.updateCredentials(response.Credentials, response.Status.CredentialID, false)
	u.showStatus(response.Status)
}

func (u *clientUI) finish() {
	u.mu.Lock()
	response := u.response
	err := u.err
	action := u.action
	targetID := u.targetID
	u.busy = false
	u.targetID = ""
	u.mu.Unlock()
	if err != nil {
		u.logger.Error("UI operation failed", "action", action, "error", err)
		u.statusShown = false
		status := "后台服务未响应，请在 Windows“服务”中重启 EZGameLAN。"
		setText(u.statusLabel, status)
		updateTrayIcon(u.hwnd, nimModify, status)
		u.setButtonsForStatus(u.lastStatus)
		return
	}
	preferredID := response.Status.CredentialID
	forceSelection := action == ipc.ActionJoin || action == ipc.ActionConnect ||
		action == ipc.ActionDeleteCredential
	if !response.OK && targetID != "" {
		preferredID = targetID
	}
	u.updateCredentials(response.Credentials, preferredID, forceSelection)
	if !response.OK {
		u.logger.Warn(
			"UI operation rejected",
			"action", action,
			"code", response.Code,
		)
		u.lastStatus = response.Status
		u.statusShown = true
		setText(u.statusLabel, response.Message)
		updateTrayIcon(u.hwnd, nimModify, response.Message)
		u.setButtonsForStatus(response.Status)
		return
	}
	if action == ipc.ActionDiagnostics {
		u.lastStatus = response.Status
		u.setButtonsForStatus(response.Status)
		u.exportDiagnostics(response)
		return
	}
	if action != ipc.ActionStatus {
		u.logger.Info("UI operation completed", "action", action)
	}
	u.showStatus(response.Status)
}

func (u *clientUI) showStatus(status ipc.Status) {
	previous := u.lastStatus
	changed := !u.statusShown || previous != status
	u.lastStatus = status
	u.setButtonsForStatus(status)
	if !changed {
		return
	}
	u.statusShown = true
	if previous != status {
		u.logger.Info(
			"connection status changed",
			"state", status.State,
			"virtual_ip", status.VirtualIP,
			"adapter", status.AdapterName,
			"error_code", status.ErrorCode,
		)
	}
	text := u.statusText(status)
	setText(u.statusLabel, text)
	updateTrayIcon(u.hwnd, nimModify, text)
}

func (u *clientUI) setButtons(enabled bool) {
	value := uintptr(0)
	if enabled {
		value = 1
	}
	for _, handle := range []uintptr{
		u.credentialList,
		u.joinButton,
		u.connectButton,
		u.disconnectButton,
		u.deleteButton,
		u.exportButton,
	} {
		if handle != 0 {
			procEnableWindow.Call(handle, value)
		}
	}
}

func (u *clientUI) setButtonsForStatus(status ipc.Status) {
	selected, hasSelection := u.selectedCredential()
	joinEnabled := status.State != ipc.StateStopping
	listEnabled := status.State != ipc.StateStopping
	connectEnabled := hasSelection
	disconnectEnabled := false
	deleteEnabled := hasSelection && status.State != ipc.StateStopping
	switch status.State {
	case ipc.StateNotConfigured:
		connectEnabled = false
	case ipc.StateDisconnected:
	case ipc.StateError:
		disconnectEnabled = status.CredentialID != ""
	case ipc.StateConnecting, ipc.StateConnected:
		disconnectEnabled = true
		connectEnabled = hasSelection && selected.ID != status.CredentialID
	case ipc.StateStopping:
		connectEnabled = false
		disconnectEnabled = false
		deleteEnabled = false
	}
	setButtonEnabled(u.credentialList, listEnabled)
	setButtonEnabled(u.joinButton, joinEnabled)
	setButtonEnabled(u.connectButton, connectEnabled)
	setButtonEnabled(u.disconnectButton, disconnectEnabled)
	setButtonEnabled(u.deleteButton, deleteEnabled)
	setButtonEnabled(u.exportButton, true)
}

func setButtonEnabled(handle uintptr, enabled bool) {
	value := uintptr(0)
	if enabled {
		value = 1
	}
	if handle != 0 {
		procEnableWindow.Call(handle, value)
	}
}

func (u *clientUI) updateCredentials(
	credentials []ipc.Credential,
	preferredID string,
	forceSelection bool,
) {
	selectedID := ""
	if selected, ok := u.selectedCredential(); ok {
		selectedID = selected.ID
	}
	changed := !credentialListsEqual(u.credentials, credentials)
	if changed {
		u.statusShown = false
		u.credentials = append([]ipc.Credential(nil), credentials...)
		procSendMessage.Call(u.credentialList, lbResetContent, 0, 0)
		for _, credential := range u.credentials {
			text := utf16(credentialDisplay(credential))
			procSendMessage.Call(
				u.credentialList,
				lbAddString,
				0,
				uintptr(unsafe.Pointer(text)),
			)
		}
	}
	if forceSelection {
		selectedID = preferredID
	}
	index := credentialIndex(u.credentials, selectedID)
	if index < 0 {
		index = credentialIndex(u.credentials, preferredID)
	}
	if index < 0 && len(u.credentials) > 0 {
		index = 0
	}
	if index >= 0 {
		procSendMessage.Call(u.credentialList, lbSetCurSel, uintptr(index), 0)
	}
}

func credentialListsEqual(left, right []ipc.Credential) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func credentialIndex(credentials []ipc.Credential, id string) int {
	if id == "" {
		return -1
	}
	for index, credential := range credentials {
		if credential.ID == id {
			return index
		}
	}
	return -1
}

func credentialDisplay(credential ipc.Credential) string {
	return credentialDisplayLimit(credential, 78)
}

func credentialDisplayLimit(credential ipc.Credential, limit int) string {
	if credential.Legacy {
		return "旧版局域网配置"
	}
	text := credential.Endpoint + "  |  " + credential.ServerName +
		"  |  " + credential.ClientName
	characters := []rune(text)
	if len(characters) <= limit {
		return text
	}
	return string(characters[:limit-3]) + "..."
}

func (u *clientUI) credentialName(id string) string {
	index := credentialIndex(u.credentials, id)
	if index < 0 {
		return ""
	}
	return credentialDisplayLimit(u.credentials[index], 58)
}

func (u *clientUI) statusText(status ipc.Status) string {
	credentialName := u.credentialName(status.CredentialID)
	switch status.State {
	case ipc.StateNotConfigured:
		return "尚无已保存的局域网凭证。"
	case ipc.StateDisconnected:
		if credentialName != "" {
			return "已断开：" + credentialName
		}
		return "当前未连接局域网。"
	case ipc.StateConnecting:
		detail := "正在连接局域网…"
		if credentialName != "" {
			detail += "\r\n" + credentialName
		}
		return detail
	case ipc.StateConnected:
		detail := "已连接到虚拟局域网"
		if credentialName != "" {
			detail += "：" + credentialName
		}
		if status.VirtualIP != "" {
			detail += "\r\n虚拟 IP：" + status.VirtualIP
		}
		if status.AdapterName != "" {
			detail += "\r\n虚拟网卡：" + status.AdapterName
		}
		return detail
	case ipc.StateStopping:
		return "正在断开…"
	case ipc.StateError:
		if status.ErrorCode != "" {
			return "连接出现问题（" + status.ErrorCode + "）。\r\n" +
				"请选择该局域网重试，或导出日志进行检查。"
		}
		return "连接出现问题。\r\n请选择局域网重试，或导出日志进行检查。"
	default:
		return "状态未知。"
	}
}

func (u *clientUI) exportDiagnostics(response ipc.Response) {
	u.logger.Info("diagnostics export requested")
	path, selected := chooseDiagnosticsPath(u.hwnd)
	if !selected {
		u.logger.Info("diagnostics export canceled")
		setText(u.statusLabel, "已取消导出。\r\n"+u.statusText(response.Status))
		return
	}
	diagnostics := response.Diagnostics
	if u.logFile != nil {
		recent, err := u.logFile.Snapshot(32 << 10)
		if err != nil {
			u.logger.Warn("client log snapshot failed", "error", err)
		} else {
			diagnostics += "\r\n\r\n=== recent bounded client UI log ===\r\n" + recent
		}
	}
	if u.logInitErr != nil {
		diagnostics += "\r\nclient_ui_log_initialization=failed\r\n"
	}
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte(diagnostics)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		u.logger.Error("diagnostics export failed", "code", "E_INTERNAL_EXPORT")
		status := "保存日志失败，请选择其他目录后重试。"
		setText(u.statusLabel, status)
		updateTrayIcon(u.hwnd, nimModify, status)
		return
	}
	u.logger.Info("diagnostics export completed")
	setText(u.statusLabel, "日志已导出：\r\n"+path)
	updateTrayIcon(u.hwnd, nimModify, "EZGameLAN 日志已导出")
}

func initializeClientLogging() {
	ui.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	localAppData := os.Getenv("LOCALAPPDATA")
	if !filepath.IsAbs(localAppData) {
		ui.logInitErr = fmt.Errorf("LOCALAPPDATA 路径无效")
		return
	}
	logFile, err := logging.OpenUser(
		filepath.Join(localAppData, "EZGameLAN", "logs", "client.log"),
	)
	if err != nil {
		ui.logInitErr = err
		return
	}
	ui.logFile = logFile
	ui.logger = slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

func chooseDiagnosticsPath(hwnd uintptr) (string, bool) {
	filename := "EZGameLAN-diagnostics-" + time.Now().Format("20060102-150405") + ".txt"
	buffer := make([]uint16, 32<<10)
	copy(buffer, windows.StringToUTF16(filename))
	filter := multiString(
		"EZGameLAN 诊断文本 (*.txt)",
		"*.txt",
		"所有文件 (*.*)",
		"*.*",
	)
	dialog := openFileName{
		Size:             uint32(unsafe.Sizeof(openFileName{})),
		Owner:            hwnd,
		Filter:           &filter[0],
		FilterIndex:      1,
		File:             &buffer[0],
		MaxFile:          uint32(len(buffer)),
		Title:            utf16("导出 EZGameLAN 诊断日志"),
		Flags:            ofnOverwritePrompt | ofnNoChangeDir | ofnPathMustExist,
		DefaultExtension: utf16("txt"),
	}
	result, _, _ := procGetSaveFileName.Call(uintptr(unsafe.Pointer(&dialog)))
	if result == 0 {
		return "", false
	}
	return windows.UTF16ToString(buffer), true
}

func multiString(values ...string) []uint16 {
	result := make([]uint16, 0)
	for _, value := range values {
		result = append(result, windows.StringToUTF16(value)...)
	}
	return append(result, 0)
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
