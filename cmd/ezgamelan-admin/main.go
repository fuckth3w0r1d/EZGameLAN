package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/r3t2/ezgamelan/internal/adminweb"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		notifyError(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("ezgamelan-admin", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	serverAlias := flags.String("server", "s0", "default OpenSSH host alias")
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	server, err := adminweb.Start(ctx, remote, *serverAlias)
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
