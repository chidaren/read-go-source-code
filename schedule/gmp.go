package main

import (
	"fmt"
	"runtime"
	"time"
)

/*

一般情况下, 如果G比较少的时候(这时候G会放在P的局部队列, 不会放到全局队列), 一般不会创建多个P(或者我们指定了最多一个P), 那么这些G基本上都是加到P的一个FIFO队列里, 如果头部的G, 哪一个死循环了(没有io, channel 调用, 文件读写, time.Sleep 等操作)
那么再这个P上, 队列里后面的G, 不会有任何机会得到调用(除非有多个P, 某一个P 上已经没有G了,M会从其他P上偷一些G, 如果偷不到, 会从全局队列获取到局部队列来执行)

G是抢占式调度, 没有时间片的概念,M对它绑定的P是有时间片的概念的, 但是如果只有一个P, 那么就相当于所有的G 都是抢占式调用, 如果一个G没有因为io或者channel 被阻塞, 那么基本上
它会一直占用着这个P

*/

// 如果B先于A被初始化, 那么会先输出0, 然后阻塞, A被调用, 一直占着P
func main() {
	var count int64

	runtime.GOMAXPROCS(1)

	// A
	go func() {
		for {
			// 如果只有一个逻辑P, 则一旦调用此G, 则不会有任何机会让出,　除非显示调用　runtime.Gosched()
			count++
		}
	}()

	// B
	go func() {
		for {
			// 如果一个P的时候，一旦死循环的G被调用，则此G不会再有任何机会被调用, 如果此G后调用, 则会加到p 的尾部, 上面的G会先被执行, 此G不会有任何机会被调用
			fmt.Println(count)
			time.Sleep(time.Second)
		}
	}()

	time.Sleep(time.Hour)
}
