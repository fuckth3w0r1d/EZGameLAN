//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/r3t2/ezgamelan/internal/adminweb"
	"golang.org/x/sys/windows"
)

const (
	promptWindowClass = "EZGameLANAdminConnectWindow"
	promptWindowTitle = "EZGameLAN 管理端"

	wmCreate  = 0x0001
	wmDestroy = 0x0002
	wmClose   = 0x0010
	wmCommand = 0x0111
	wmSetFont = 0x0030

	wsChild       = 0x40000000
	wsVisible     = 0x10000000
	wsBorder      = 0x00800000
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsTabStop     = 0x00010000
	esAutoHScroll = 0x0080

	bsDefPushButton = 0x00000001
	colorWindow     = 5
	idcArrow        = 32512
	defaultGUIFont  = 17
	emSetLimitText  = 0x00C5
	emSetCueBanner  = 0x1501

	idPromptConnect = 3001
	idPromptCancel  = 3002
)

var (
	promptUser32 = windows.NewLazySystemDLL("user32.dll")
	promptGDI32  = windows.NewLazySystemDLL("gdi32.dll")

	promptRegisterClass = promptUser32.NewProc("RegisterClassExW")
	promptCreateWindow  = promptUser32.NewProc("CreateWindowExW")
	promptDefWindowProc = promptUser32.NewProc("DefWindowProcW")
	promptShowWindow    = promptUser32.NewProc("ShowWindow")
	promptUpdateWindow  = promptUser32.NewProc("UpdateWindow")
	promptGetMessage    = promptUser32.NewProc("GetMessageW")
	promptTranslate     = promptUser32.NewProc("TranslateMessage")
	promptDispatch      = promptUser32.NewProc("DispatchMessageW")
	promptPostQuit      = promptUser32.NewProc("PostQuitMessage")
	promptLoadCursor    = promptUser32.NewProc("LoadCursorW")
	promptDestroyWindow = promptUser32.NewProc("DestroyWindow")
	promptSetText       = promptUser32.NewProc("SetWindowTextW")
	promptGetText       = promptUser32.NewProc("GetWindowTextW")
	promptGetTextLength = promptUser32.NewProc("GetWindowTextLengthW")
	promptSendMessage   = promptUser32.NewProc("SendMessageW")
	promptSetFocus      = promptUser32.NewProc("SetFocus")
	promptGetStock      = promptGDI32.NewProc("GetStockObject")

	promptClassOnce sync.Once
	promptClassErr  error
	currentPrompt   promptState
)

type promptState struct {
	initial   string
	problem   string
	edit      uintptr
	status    uintptr
	target    string
	confirmed bool
}

type promptPoint struct {
	X int32
	Y int32
}

type promptMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   promptPoint
	Private uint32
}

type promptWindowClassEx struct {
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

func promptServer(initial, problem string) (string, bool, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	promptClassOnce.Do(registerPromptClass)
	if promptClassErr != nil {
		return "", false, promptClassErr
	}
	currentPrompt = promptState{
		initial: initial,
		problem: shortPromptError(problem),
	}
	window, _, callErr := promptCreateWindow.Call(
		0,
		uintptr(unsafe.Pointer(promptUTF16(promptWindowClass))),
		uintptr(unsafe.Pointer(promptUTF16(promptWindowTitle))),
		wsCaption|wsSysMenu,
		0x80000000,
		0x80000000,
		580,
		290,
		0,
		0,
		0,
		0,
	)
	if window == 0 {
		return "", false, fmt.Errorf("创建 SSH 连接窗口: %w", callErr)
	}
	promptShowWindow.Call(window, 5)
	promptUpdateWindow.Call(window)

	var message promptMessage
	for {
		result, _, err := promptGetMessage.Call(
			uintptr(unsafe.Pointer(&message)),
			0,
			0,
			0,
		)
		if int32(result) == -1 {
			return "", false, fmt.Errorf("读取 SSH 连接窗口消息: %w", err)
		}
		if result == 0 {
			break
		}
		promptTranslate.Call(uintptr(unsafe.Pointer(&message)))
		promptDispatch.Call(uintptr(unsafe.Pointer(&message)))
	}
	return currentPrompt.target, currentPrompt.confirmed, nil
}

func registerPromptClass() {
	cursor, _, _ := promptLoadCursor.Call(0, idcArrow)
	class := promptWindowClassEx{
		Size:       uint32(unsafe.Sizeof(promptWindowClassEx{})),
		WindowProc: windows.NewCallback(promptWindowProc),
		Cursor:     cursor,
		Background: colorWindow + 1,
		ClassName:  promptUTF16(promptWindowClass),
	}
	atom, _, err := promptRegisterClass.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		promptClassErr = fmt.Errorf("注册 SSH 连接窗口: %w", err)
	}
}

func promptWindowProc(window uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmCreate:
		createPromptControls(window)
		return 0
	case wmCommand:
		switch uint16(wParam & 0xffff) {
		case idPromptConnect:
			target := strings.TrimSpace(
				promptControlText(currentPrompt.edit, 128),
			)
			if err := adminweb.ValidateServerAlias(target); err != nil {
				promptSetControlText(currentPrompt.status, err.Error())
				return 0
			}
			currentPrompt.target = target
			currentPrompt.confirmed = true
			promptDestroyWindow.Call(window)
		case idPromptCancel:
			promptDestroyWindow.Call(window)
		}
		return 0
	case wmClose:
		promptDestroyWindow.Call(window)
		return 0
	case wmDestroy:
		promptPostQuit.Call(0)
		return 0
	}
	result, _, _ := promptDefWindowProc.Call(window, uintptr(message), wParam, lParam)
	return result
}

func createPromptControls(window uintptr) {
	font, _, _ := promptGetStock.Call(defaultGUIFont)
	promptCreateLabel(
		window,
		"输入 SSH 配置别名或 USER@HOST。连接验证成功后自动打开管理页面。",
		24, 20, 520, 38, font,
	)
	promptCreateLabel(window, "SSH 服务器", 24, 66, 120, 22, font)
	currentPrompt.edit = promptCreateControl(
		"EDIT",
		currentPrompt.initial,
		wsChild|wsVisible|wsBorder|wsTabStop|esAutoHScroll,
		24, 90, 520, 29,
		window, 0, font,
	)
	promptSendMessage.Call(currentPrompt.edit, emSetLimitText, 128, 0)
	promptSendMessage.Call(
		currentPrompt.edit,
		emSetCueBanner,
		1,
		uintptr(unsafe.Pointer(promptUTF16("例如 vpn-server 或 user@vpn.example.com"))),
	)
	currentPrompt.status = promptCreateLabel(
		window,
		currentPrompt.problem,
		24, 128, 520, 52, font,
	)
	promptCreateControl(
		"BUTTON",
		"取消",
		wsChild|wsVisible|wsTabStop,
		356, 190, 88, 32,
		window, idPromptCancel, font,
	)
	promptCreateControl(
		"BUTTON",
		"连接",
		wsChild|wsVisible|wsTabStop|bsDefPushButton,
		456, 190, 88, 32,
		window, idPromptConnect, font,
	)
	promptSetFocus.Call(currentPrompt.edit)
}

func promptCreateLabel(
	parent uintptr,
	text string,
	x, y, width, height int32,
	font uintptr,
) uintptr {
	return promptCreateControl(
		"STATIC",
		text,
		wsChild|wsVisible,
		x, y, width, height,
		parent, 0, font,
	)
}

func promptCreateControl(
	class, text string,
	style uint32,
	x, y, width, height int32,
	parent uintptr,
	id uint16,
	font uintptr,
) uintptr {
	control, _, _ := promptCreateWindow.Call(
		0,
		uintptr(unsafe.Pointer(promptUTF16(class))),
		uintptr(unsafe.Pointer(promptUTF16(text))),
		uintptr(style),
		uintptr(x),
		uintptr(y),
		uintptr(width),
		uintptr(height),
		parent,
		uintptr(id),
		0,
		0,
	)
	if control != 0 {
		promptSendMessage.Call(control, wmSetFont, font, 1)
	}
	return control
}

func promptControlText(control uintptr, limit int) string {
	length, _, _ := promptGetTextLength.Call(control)
	if int(length) > limit {
		length = uintptr(limit)
	}
	buffer := make([]uint16, int(length)+1)
	promptGetText.Call(
		control,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	return windows.UTF16ToString(buffer)
}

func promptSetControlText(control uintptr, value string) {
	promptSetText.Call(control, uintptr(unsafe.Pointer(promptUTF16(value))))
}

func promptUTF16(value string) *uint16 {
	pointer, _ := windows.UTF16PtrFromString(value)
	return pointer
}
