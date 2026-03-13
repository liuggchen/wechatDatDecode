package main

import (
	"bytes"
	"crypto/aes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

type FileHeader struct {
	Magic []byte
	Ext   string
}

type DecodeResult struct {
	Data   []byte
	Ext    string
	Mode   string
	XorKey byte
}

var headers = []FileHeader{
	{[]byte{0xFF, 0xD8, 0xFF}, ".jpg"},
	{[]byte{0x89, 0x50, 0x4E, 0x47}, ".png"},
	{[]byte("GIF8"), ".gif"},
	{[]byte{0x49, 0x49, 0x2A, 0x00}, ".tif"},
	{[]byte("BM"), ".bmp"},
	{[]byte("RIFF"), ".webp"},
	{[]byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70}, ".mp4"},
}

var v1Signature = []byte{0x07, 0x08, 0x56, 0x31, 0x08, 0x07}
var v2Signature = []byte{0x07, 0x08, 0x56, 0x32, 0x08, 0x07}
var v1AESKey = []byte("cfcd208495d565ef")

func main() {
	var inputDir string
	var aesKey string

	flag.StringVar(&inputDir, "in", ".", "dat 目录路径")
	flag.StringVar(&aesKey, "key", "", "V2 的 AES key，可传 16 字节文本或 32 位 hex")
	flag.Parse()

	if inputDir == "" && flag.NArg() > 0 {
		inputDir = flag.Arg(0)
	}
	if inputDir == "" {
		fmt.Println("usage: decode -in dat_dir -key your_aes_key")
		return
	}
	if _, err := parseAESKey(aesKey); err != nil {
		fmt.Println("参数 -key 必传，且必须是 16 字节文本或 32 位 hex")
		fmt.Println("usage: decode -in dat_dir -key your_aes_key")
		return
	}

	info, err := os.Stat(inputDir)
	if err != nil {
		fmt.Println("读取目录失败:", err.Error())
		return
	}
	if !info.IsDir() {
		fmt.Println("参数 -in 必须是目录")
		return
	}

	outputDir := filepath.Join(".", "output")
	err = os.MkdirAll(outputDir, 0755)
	if err != nil {
		fmt.Println("创建输出目录失败:", err.Error())
		return
	}

	successCount := 0
	failCount := 0
	_ = filepath.Walk(inputDir, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			fmt.Println("遍历失败:", walkErr.Error())
			failCount++
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		if strings.ToLower(filepath.Ext(fi.Name())) != ".dat" {
			return nil
		}

		ok := processOneFile(path, outputDir, aesKey)
		if ok {
			successCount++
		} else {
			failCount++
		}
		return nil
	})

	fmt.Printf("finished. success=%d fail=%d output_dir=%s\n", successCount, failCount, outputDir)
}

func processOneFile(inputPath, outputDir, aesKey string) bool {
	data, err := ioutil.ReadFile(inputPath)
	if err != nil {
		fmt.Printf("decode fail: %s, err=%s\n", inputPath, err.Error())
		return false
	}

	result, err := decodeData(data, aesKey)
	if err != nil {
		fmt.Printf("decode fail: %s, err=%s\n", inputPath, err.Error())
		return false
	}

	out := buildOutputPath(inputPath, outputDir, result.Ext)
	err = ioutil.WriteFile(out, result.Data, 0644)
	if err != nil {
		fmt.Printf("write fail: %s, err=%s\n", out, err.Error())
		return false
	}

	fmt.Println("decode success:", out)
	fmt.Println("mode:", result.Mode)
	fmt.Printf("xor key: 0x%02X\n", result.XorKey)
	return true
}

func decodeData(data []byte, aesKey string) (DecodeResult, error) {
	if len(data) == 0 {
		return DecodeResult{}, errors.New("empty file")
	}

	if bytes.HasPrefix(data, v1Signature) {
		return decodeVx(data, v1AESKey, "v1")
	}

	if bytes.HasPrefix(data, v2Signature) {
		key, err := parseAESKey(aesKey)
		if err != nil {
			return DecodeResult{}, errors.New("检测到 V2 格式，需要通过 -key 传入 AES key（16 字节文本或 32 位 hex）")
		}
		return decodeVx(data, key, "v2")
	}

	return decodeOldXOR(data)
}

func decodeOldXOR(data []byte) (DecodeResult, error) {
	for _, h := range headers {
		if len(data) < len(h.Magic) {
			continue
		}

		key := data[0] ^ h.Magic[0]
		decoded := xorBytes(data, key)
		ext, ok := detectExt(decoded)
		if ok {
			return DecodeResult{
				Data:   decoded,
				Ext:    ext,
				Mode:   "old-xor",
				XorKey: key,
			}, nil
		}
		if len(h.Magic) >= 3 && bytes.Equal(decoded[:len(h.Magic)], h.Magic) {
			return DecodeResult{
				Data:   decoded,
				Ext:    h.Ext,
				Mode:   "old-xor",
				XorKey: key,
			}, nil
		}
	}

	return DecodeResult{}, errors.New("未识别到可解密格式")
}

func decodeVx(data []byte, aesKey []byte, mode string) (DecodeResult, error) {
	if len(data) < 15 {
		return DecodeResult{}, errors.New("文件过短，格式无效")
	}

	aesSize := int(binary.LittleEndian.Uint32(data[6:10]))
	xorSize := int(binary.LittleEndian.Uint32(data[10:14]))
	marker := data[14]
	body := data[15:]

	if aesSize < 0 || xorSize < 0 || aesSize+xorSize > len(body) {
		return DecodeResult{}, errors.New("分段长度异常，无法解密")
	}
	if aesSize%aes.BlockSize != 0 {
		return DecodeResult{}, errors.New("AES 分段长度不是 16 的倍数")
	}

	rawSize := len(body) - aesSize - xorSize
	aesEncrypted := body[:aesSize]
	rawPart := body[aesSize : aesSize+rawSize]
	xorEncrypted := body[aesSize+rawSize:]

	aesPart, err := decryptECB(aesEncrypted, aesKey)
	if err != nil {
		return DecodeResult{}, err
	}
	aesPart = trimPKCS7(aesPart)

	xorKey := marker
	ext, ok := detectExt(aesPart)
	if ok {
		if k, kOK := guessTailXORKey(ext, xorEncrypted); kOK {
			xorKey = k
		}
	}

	decoded := buildDecoded(aesPart, rawPart, xorEncrypted, xorKey)
	bestKey, bestDecoded := findBestDecodedCandidate(aesPart, rawPart, xorEncrypted, ext, ok, decoded, xorKey)
	xorKey = bestKey
	decoded = bestDecoded

	if finalExt, finalOK := detectExt(decoded); finalOK {
		ext = finalExt
	} else if !ok {
		ext = ".bin"
	}

	return DecodeResult{
		Data:   decoded,
		Ext:    ext,
		Mode:   mode,
		XorKey: xorKey,
	}, nil
}

func parseAESKey(key string) ([]byte, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("empty")
	}
	if len(key) == 32 {
		b, err := hex.DecodeString(key)
		if err == nil && len(b) == 16 {
			return b, nil
		}
	}
	if len(key) == 16 {
		return []byte(key), nil
	}
	return nil, errors.New("invalid")
}

func decryptECB(ciphertext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext 不是 16 的倍数")
	}

	out := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}
	return out, nil
}

func trimPKCS7(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	pad := int(data[len(data)-1])
	if pad < 1 || pad > aes.BlockSize || len(data) < pad {
		return data
	}
	for i := 0; i < pad; i++ {
		if data[len(data)-1-i] != byte(pad) {
			return data
		}
	}
	return data[:len(data)-pad]
}

func buildDecoded(aesPart, rawPart, xorEncrypted []byte, xorKey byte) []byte {
	xorPart := xorBytes(xorEncrypted, xorKey)
	decoded := make([]byte, 0, len(aesPart)+len(rawPart)+len(xorPart))
	decoded = append(decoded, aesPart...)
	decoded = append(decoded, rawPart...)
	decoded = append(decoded, xorPart...)
	return decoded
}

func findBestDecodedCandidate(aesPart, rawPart, xorEncrypted []byte, ext string, hasExt bool, initialDecoded []byte, initialKey byte) (byte, []byte) {
	if len(xorEncrypted) == 0 {
		return initialKey, initialDecoded
	}

	bestKey := initialKey
	bestDecoded := initialDecoded
	bestScore := scoreDecoded(initialDecoded, ext, hasExt)

	for k := 0; k < 256; k++ {
		key := byte(k)
		decoded := buildDecoded(aesPart, rawPart, xorEncrypted, key)
		score := scoreDecoded(decoded, ext, hasExt)
		if score > bestScore {
			bestScore = score
			bestKey = key
			bestDecoded = decoded
		}
	}

	return bestKey, bestDecoded
}

func scoreDecoded(decoded []byte, expectedExt string, hasExpectedExt bool) int {
	score := 0

	detectedExt, ok := detectExt(decoded)
	if ok {
		score += 100
		if hasExpectedExt && detectedExt == expectedExt {
			score += 80
		}
		switch detectedExt {
		case ".jpg":
			if bytes.HasSuffix(decoded, []byte{0xFF, 0xD9}) {
				score += 80
			}
		case ".png":
			if bytes.HasSuffix(decoded, []byte{0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}) {
				score += 80
			}
		case ".gif":
			if len(decoded) > 0 && decoded[len(decoded)-1] == 0x3B {
				score += 80
			}
		}
	}

	return score
}

func guessTailXORKey(ext string, tail []byte) (byte, bool) {
	if len(tail) == 0 {
		return 0, false
	}

	switch ext {
	case ".jpg":
		if len(tail) < 2 {
			return 0, false
		}
		k := tail[len(tail)-1] ^ 0xD9
		if tail[len(tail)-2]^k == 0xFF {
			return k, true
		}
	case ".png":
		sig := []byte{0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}
		if len(tail) < len(sig) {
			return 0, false
		}
		start := len(tail) - len(sig)
		k := tail[start] ^ sig[0]
		for i := 1; i < len(sig); i++ {
			if tail[start+i]^k != sig[i] {
				return 0, false
			}
		}
		return k, true
	case ".gif":
		k := tail[len(tail)-1] ^ 0x3B
		return k, true
	}

	return 0, false
}

func detectExt(data []byte) (string, bool) {
	for _, h := range headers {
		if len(data) < len(h.Magic) {
			continue
		}
		if !bytes.Equal(data[:len(h.Magic)], h.Magic) {
			continue
		}
		if h.Ext == ".bmp" && len(data) >= 10 {
			if binary.LittleEndian.Uint32(data[6:10]) != 0 {
				continue
			}
		}
		return h.Ext, true
	}
	return "", false
}

func xorBytes(data []byte, key byte) []byte {
	out := make([]byte, len(data))
	for i := range data {
		out[i] = data[i] ^ key
	}
	return out
}

func buildOutputPath(inputPath, outputDir, ext string) string {
	base := filepath.Base(inputPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(outputDir, name+ext)
}
