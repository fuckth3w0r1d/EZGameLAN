package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/r3t2/ezgamelan/internal/admin"
	"github.com/r3t2/ezgamelan/internal/adminweb"
)

const sshProbeTimeout = 20 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		notifyError(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("ezgamelan-admin", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	serverTarget := flags.String("server", "", "OpenSSH host alias or USER@HOST")
	sshPath := flags.String("ssh", "", "OpenSSH client path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("管理端不接受位置参数")
	}

	remote, err := adminweb.NewSSHRemote(*sshPath)
	if err != nil {
		return err
	}
	initial := *serverTarget
	problem := ""
	if initial == "" {
		initial, err = adminweb.LoadServerPreference()
		if err != nil {
			problem = "读取上次连接设置失败，请重新输入 SSH 服务器。"
			initial = ""
		}
	}
	connectedTarget, cancelled, err := connectServer(remote, initial, problem)
	if err != nil {
		return err
	}
	if cancelled {
		return nil
	}
	_ = adminweb.SaveServerPreference(connectedTarget)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	server, err := adminweb.Start(ctx, remote, connectedTarget)
	if err != nil {
		return err
	}
	if err := adminweb.OpenBrowser(server.URL()); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Close(shutdownCtx)
		return fmt.Errorf("%w；管理地址：%s", err, server.URL())
	}
	if err := server.Wait(); err != nil {
		return fmt.Errorf("本机管理界面: %w", err)
	}
	return nil
}

func connectServer(
	remote *adminweb.SSHRemote,
	initial, problem string,
) (string, bool, error) {
	for {
		target, confirmed, err := promptServer(initial, problem)
		if err != nil {
			return "", false, err
		}
		if !confirmed {
			return "", true, nil
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), sshProbeTimeout)
		response, probeErr := remote.Call(probeCtx, target, admin.Request{
			ProtocolVersion: admin.ProtocolVersion,
			Action:          admin.ActionSnapshot,
		})
		cancel()
		if probeErr == nil && response.OK {
			return target, false, nil
		}
		initial = target
		if probeErr != nil {
			problem = shortPromptError(probeErr.Error())
			continue
		}
		if response.Error != nil {
			problem = shortPromptError(response.Error.Message)
		} else {
			problem = "服务器拒绝了管理连接，请检查服务端配置。"
		}
	}
}

func shortPromptError(message string) string {
	runes := []rune(message)
	const limit = 220
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return message
}
