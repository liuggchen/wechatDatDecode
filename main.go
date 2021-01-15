package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
)

//JPEG (jpg)，文件头：FFD8FF
//PNG (png)，文件头：89504E47
//GIF (gif)，文件头：47494638
//TIFF (tif)，文件头：49492A00
//Windows Bitmap (bmp)，文件头：424D
const (
	Jpeg = "FFD8FF"
	Png  = "89504E47"
	Gif  = "47494638"
	Tif  = "49492A00"
	Bmp  = "424D"
)

func main() {
	in := flag.String("in", ".", "要处理的目录")
	out := flag.String("out", "./Decode", "要输出的目录")
	flag.Parse()
	fmt.Println("处理目录：", *in)
	fmt.Println("输出目录目录：", *out)
	var dir = *in
	var outputDir = *out
	f, er := os.Open(dir)
	if er != nil {
		fmt.Println(er.Error())
		panic("dir not find")
	}
	readdir, er := f.Readdir(0)
	if er != nil {
		fmt.Println(er.Error())
	}
	stat, er := os.Stat(outputDir)

	if er != nil {
		er := os.Mkdir(outputDir, 0755)
		if er != nil {
			panic("create dir: " + outputDir + " fail")
		}
	} else if !stat.IsDir() {
		panic(outputDir + "is file")
	}

	var taskChan = make(chan os.FileInfo, 100)

	go func() {
		for _, info := range readdir {
			taskChan <- info
		}
		close(taskChan)
	}()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if info, ok := <-taskChan; ok {
					handlerOne(info, dir, outputDir)
				} else {
					break
				}
			}
		}()
	}

	wg.Wait()
	fmt.Println("全部解码完成")
}

func handlerOne(info os.FileInfo, dir string, outputDir string, ) {
	if info.IsDir() || filepath.Ext(info.Name()) != ".dat" {
		return
	}
	fmt.Println("find file: ", info.Name())
	fPath := dir + "/" + info.Name()
	bts, er := ioutil.ReadFile(fPath)
	if er != nil {
		fmt.Println(er.Error())
	}
	deCodeByte, ext, er := handlerImg(bts)
	if er != nil {
		fmt.Println(er.Error())
		return
	}

	f, er := os.Create(outputDir + "/" + info.Name() + ext)
	if er != nil {
		fmt.Println(er.Error())
		return
	}
	for _, bt := range bts {
		_, er := f.Write([]byte{bt ^ deCodeByte})
		if er != nil {
			fmt.Println(er.Error())
		}
	}
	_ = f.Close()

	fmt.Println("输出文件：", f.Name())
}

func handlerImg(bts []byte) (byte, string, error) {
	JpegPrefixBytes, _ := hex.DecodeString(Jpeg)
	PngPrefixBytes, _ := hex.DecodeString(Png)
	GifPrefixBytes, _ := hex.DecodeString(Gif)
	TifPrefixBytes, _ := hex.DecodeString(Tif)
	BmpPrefixBytes, _ := hex.DecodeString(Bmp)

	prefixMap := map[string][]byte{
		".jpeg": JpegPrefixBytes,
		".png":  PngPrefixBytes,
		".gif":  GifPrefixBytes,
		".tif":  TifPrefixBytes,
		".bmp":  BmpPrefixBytes,
	}

	for ext, prefixBytes := range prefixMap {
		deCodeByte, ext, err := handlerPrefix(prefixBytes, ext, bts)
		if err == nil {
			return deCodeByte, ext, err
		}
	}
	return 0, "", errors.New("文件处理失败")
}

func handlerPrefix(JpegPrefixBytes []byte, suffix string, bts []byte) (deCodeByte byte, ext string, error error) {
	var initDecodeByte = JpegPrefixBytes[0] ^ bts[0]
	for i, prefixByte := range JpegPrefixBytes {
		deCodeByte := prefixByte ^ bts[i]
		if deCodeByte != initDecodeByte {
			return 0, suffix, errors.New("NOT jpeg")
		}
	}
	return initDecodeByte, suffix, nil
}
