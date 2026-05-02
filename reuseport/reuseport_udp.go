package reuseport

import (
	"fmt"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func ListenUDPWithReusePort(network, addr string) (*net.UDPConn, error) {
	// 1. 解析 UDP 地址
	udpAddr, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, fmt.Errorf("resolve addr err: %w", err)
	}

	// 2. 确定协议族
	var domain int
	if udpAddr.IP.To4() == nil {
		domain = unix.AF_INET6
	} else {
		domain = unix.AF_INET
	}

	// 3. 创建 socket（不设置 SOCK_NONBLOCK/SOCK_CLOEXEC）
	fd, err := unix.Socket(domain, unix.SOCK_DGRAM, unix.IPPROTO_UDP)
	if err != nil {
		return nil, fmt.Errorf("socket create err: %w", err)
	}
	defer func() {
		if err != nil {
			unix.Close(fd)
		}
	}()

	// 4. 设置 SO_REUSEPORT（跨平台共用）
	if err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
		return nil, fmt.Errorf("setsockopt SO_REUSEPORT err: %w", err)
	}

	// 5. 设置非阻塞模式（跨平台方式）
	if err = unix.SetNonblock(fd, true); err != nil {
		return nil, fmt.Errorf("set nonblock err: %w", err)
	}

	// 6. 设置 close-on-exec（跨平台方式）
	if err = unix.SetNonblock(fd, true); err != nil {
		return nil, fmt.Errorf("set nonblock err: %w", err)
	}
	// 注意：SOCK_CLOEXEC 在 macOS 上需要通过 fcntl 单独设置
	// Go 的 syscall.CloseOnExec 封装了平台差异
	syscall.CloseOnExec(fd)

	// 7. 绑定地址
	switch domain {
	case unix.AF_INET:
		sockaddr := &unix.SockaddrInet4{Port: udpAddr.Port}
		copy(sockaddr.Addr[:], udpAddr.IP.To4())
		err = unix.Bind(fd, sockaddr)
	case unix.AF_INET6:
		sockaddr := &unix.SockaddrInet6{Port: udpAddr.Port}
		copy(sockaddr.Addr[:], udpAddr.IP.To16())
		err = unix.Bind(fd, sockaddr)
	}
	if err != nil {
		return nil, fmt.Errorf("bind err: %w", err)
	}

	// 8. 转换为 net.UDPConn
	file := os.NewFile(uintptr(fd), "udp")
	defer file.Close()

	conn, err := net.FilePacketConn(file)
	if err != nil {
		return nil, fmt.Errorf("convert to PacketConn err: %w", err)
	}

	return conn.(*net.UDPConn), nil
}
