package transport

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// 这一整个文件是同一套断言，跑在每个实现上。
//
// 接口只有一个实现的时候，它的形状是照着那个实现长的——哪些行为是「传输的约定」、
// 哪些只是「Unix socket 恰好是这样」，不写这么一套是分不清的。ADR-0001 要的是
// 「换实现而不动牌局逻辑」，那就得先说清楚换进来的东西要满足什么。

func implementations(t *testing.T) map[string]Transport {
	t.Helper()
	unix, err := NewUnix(t.TempDir())
	if err != nil {
		t.Fatalf("造不出同机传输: %v", err)
	}
	return map[string]Transport{
		"unix":   unix,
		"memory": NewMemory(),
	}
}

// eachTransport 把同一段测试跑在每个实现上。
func eachTransport(t *testing.T, fn func(t *testing.T, tr Transport)) {
	t.Helper()
	for name, tr := range implementations(t) {
		t.Run(name, func(t *testing.T) { fn(t, tr) })
	}
}

const testCode = "ABC234"

// TestListenThenDialCarriesBytes：连起来之后两个方向都得通。
func TestListenThenDialCarriesBytes(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr Transport) {
		ln, err := tr.Listen(testCode)
		if err != nil {
			t.Fatalf("监听失败: %v", err)
		}
		defer ln.Close()

		accepted := make(chan net.Conn, 1)
		go func() {
			c, err := ln.Accept()
			if err != nil {
				close(accepted)
				return
			}
			accepted <- c
		}()

		client, err := tr.Dial(testCode)
		if err != nil {
			t.Fatalf("拨号失败: %v", err)
		}
		defer client.Close()

		var server net.Conn
		select {
		case c, ok := <-accepted:
			if !ok {
				t.Fatal("没接到连接")
			}
			server = c
		case <-time.After(3 * time.Second):
			t.Fatal("等 Accept 超时")
		}
		defer server.Close()

		// 客户端 → 服务端
		go func() { _, _ = client.Write([]byte("ping\n")) }()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(server, buf); err != nil {
			t.Fatalf("服务端没读到: %v", err)
		}
		if string(buf) != "ping\n" {
			t.Fatalf("服务端读到 %q", buf)
		}

		// 服务端 → 客户端
		go func() { _, _ = server.Write([]byte("pong\n")) }()
		if _, err := io.ReadFull(client, buf); err != nil {
			t.Fatalf("客户端没读到: %v", err)
		}
		if string(buf) != "pong\n" {
			t.Fatalf("客户端读到 %q", buf)
		}
	})
}

// TestDialWithoutTableFails：没开的牌桌连不上，而且是报错不是挂起。
func TestDialWithoutTableFails(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr Transport) {
		if c, err := tr.Dial(testCode); err == nil {
			c.Close()
			t.Fatal("没人开桌却连上了")
		}
	})
}

// TestListenTwiceFails：同一个 Table Code 不能同时开两张桌。
//
// 服务端「生成一个码、监听、撞了就换一个」那套重试全靠这条：
// 监听失败本身就是「这个码被占了」的判据。
func TestListenTwiceFails(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr Transport) {
		ln, err := tr.Listen(testCode)
		if err != nil {
			t.Fatalf("第一次监听就失败: %v", err)
		}
		defer ln.Close()
		if second, err := tr.Listen(testCode); err == nil {
			second.Close()
			t.Fatal("同一个码开了两张桌")
		}
	})
}

// TestCloseFreesTheCode：关掉之后那个码要能再开。
//
// 同机传输如果忘了删 socket 文件，这里会红——而在真实使用中，
// 它表现为「重开牌桌报地址已被占用」，看到那条错误的人一般不会想到去 rm 一个文件。
func TestCloseFreesTheCode(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr Transport) {
		ln, err := tr.Listen(testCode)
		if err != nil {
			t.Fatalf("监听失败: %v", err)
		}
		if err := ln.Close(); err != nil {
			t.Fatalf("关不掉: %v", err)
		}
		again, err := tr.Listen(testCode)
		if err != nil {
			t.Fatalf("关掉之后再开还是失败: %v", err)
		}
		_ = again.Close()
	})
}

// TestAcceptFailsAfterClose：关掉之后 Accept 要返回错误，不能永远挂着。
//
// 服务端的 accept 循环就是靠这个退出的。
func TestAcceptFailsAfterClose(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr Transport) {
		ln, err := tr.Listen(testCode)
		if err != nil {
			t.Fatalf("监听失败: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := ln.Accept()
			done <- err
		}()
		time.Sleep(20 * time.Millisecond)
		_ = ln.Close()

		select {
		case err := <-done:
			if err == nil {
				t.Fatal("关掉之后 Accept 居然成功了")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("关掉之后 Accept 还挂着")
		}
	})
}

// TestDialAfterCloseFails：牌桌关了就连不上了。
func TestDialAfterCloseFails(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr Transport) {
		ln, err := tr.Listen(testCode)
		if err != nil {
			t.Fatalf("监听失败: %v", err)
		}
		_ = ln.Close()
		if c, err := tr.Dial(testCode); err == nil {
			c.Close()
			t.Fatal("牌桌已经关了却还连得上")
		}
	})
}

// TestBadTableCodeIsRejected：非法的 Table Code 在每个实现上都得被挡下来。
//
// 这条对同机传输是安全边界（码要拼进文件路径，藏着 ../ 就不是在找牌桌了），
// 但它不该只是同机传输的脾气：一个在这个传输上能用的客户端，换个传输也得能用。
// 各实现各一套校验，等于让「什么是合法的牌桌码」有两个答案。
func TestBadTableCodeIsRejected(t *testing.T) {
	bad := []string{"", "abc", "ABCDE1", "../etc", "AB/DEF", "ABCDE\n", "ABCDEFG"}
	eachTransport(t, func(t *testing.T, tr Transport) {
		for _, code := range bad {
			if ln, err := tr.Listen(code); err == nil {
				ln.Close()
				t.Fatalf("%q 不该开得了桌", code)
			}
			if c, err := tr.Dial(code); err == nil {
				c.Close()
				t.Fatalf("%q 不该连得上", code)
			}
		}
	})
}

// TestCodeIsCaseInsensitive：牌桌码是要口头报给别人的，大小写不该成为障碍。
func TestCodeIsCaseInsensitive(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr Transport) {
		ln, err := tr.Listen(testCode)
		if err != nil {
			t.Fatalf("监听失败: %v", err)
		}
		defer ln.Close()
		go func() {
			if c, err := ln.Accept(); err == nil {
				_ = c.Close()
			}
		}()
		c, err := tr.Dial("abc234")
		if err != nil {
			t.Fatalf("小写的码该连得上: %v", err)
		}
		_ = c.Close()
	})
}

// TestDescribeSaysSomething：开桌信息要打印给人看，不能是空的。
func TestDescribeSaysSomething(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr Transport) {
		if got := tr.Describe(testCode); got == "" {
			t.Fatal("Describe 返回了空串")
		}
	})
}

// TestUnixRemovesSocketFile：同机传输关掉之后不留下垃圾文件。
//
// 这条只对同机传输成立，所以不在上面那套通用断言里——
// 分清「传输的约定」和「这个实现恰好如此」，正是那套通用断言的意义。
func TestUnixRemovesSocketFile(t *testing.T) {
	dir := t.TempDir()
	u, err := NewUnix(dir)
	if err != nil {
		t.Fatalf("造不出同机传输: %v", err)
	}
	path, err := u.Path(testCode)
	if err != nil {
		t.Fatalf("算不出路径: %v", err)
	}

	ln, err := u.Listen(testCode)
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("监听之后 socket 文件该在：%v", err)
	}
	_ = ln.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("关掉之后 socket 文件该被删掉，%s 还在", path)
	}
}
