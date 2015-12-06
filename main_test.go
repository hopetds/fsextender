package main

import (
	"fmt"
	"github.com/rekby/pretty"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const GB = 1024 * 1024 * 1024
const TMP_DIR = "/tmp"
const TMP_MOUNT_DIR = "/tmp/fsextender-test-mount-dir"
const LVM_VG_NAME = "test-fsextender-lvm-vg"
const LVM_LV_NAME = "test-fsextender-lvm-lv"

const MSDOS_START_BYTE = 32256
const MSDOS_LAST_BYTE = 107374182399
const GPT_START_BYTE = 0x4400
const GPT_LAST_BYTE = 107374165503

var PART_TABLES = []string{"msdos", "gpt"}

type testPartition struct {
	Number uint64
	Start  uint64
	Last   uint64
}

// Call main program
func call(args ...string) {
	if os.Getuid() == 0 {
		// for test coverage run test as root
		oldArgs := os.Args
		os.Args = append([]string{os.Args[0]}, args...)

		// Clean old environment
		majorMinorDeviceTypeCache = make(map[[2]int]storageItem)
		diskNewPartitionNumLastGeneratedNum = make(map[[2]int]uint32)

		main()
		os.Args = oldArgs
	} else {
		sudo("./fsextender", args...)
	}
}

// Create loop-back test device with partition table
func createTmpDevice(partTable string) (path string, err error) {
	var f *os.File
	f, err = ioutil.TempFile(TMP_DIR, "fsextender-loop-")
	if err != nil {
		return
	}
	fname := f.Name()

	// Large sparse size = 100GB
	_, err = f.WriteAt([]byte{0}, 100*GB)
	f.Close()
	if err != nil {
		os.Remove(fname)
		return
	}

	res, errString, err := sudo("losetup", "-f", "--show", fname)
	path = strings.TrimSpace(res)
	if path == "" {
		err = fmt.Errorf("Can't create loop device: %v\n%v\n%v\n", fname, err, errString)
		os.Remove(fname)
		return
	}

	time.Sleep(time.Second)

	_, _, err = sudo("parted", "-s", path, "mklabel", partTable)
	if err != nil {
		err = fmt.Errorf("Can't create part table: %v (%v)\n", path, err)
		deleteTmpDevice(path)
		path = ""
		return
	}
	return path, nil
}

func deleteTmpDevice(path string) {
	filePath, _, _ := sudo("losetup", path)
	start := strings.Index(filePath, "(")
	finish := strings.Index(filePath, ")")
	filePath = filePath[start+1 : finish]

	cmd("sudo", "losetup", "-d", path)
	os.Remove(filePath)
}

// Return volume of filesystem in 1-Gb blocks
func df(path string) uint64 {
	res, _, _ := cmd("df", "-BG", path)
	resLines := strings.Split(res, "\n")
	blocksStart := strings.Index(resLines[0], "1G-blocks")
	blocksEnd := blocksStart + len("1G-blocks")
	blocksString := strings.TrimSpace(resLines[1][blocksStart:blocksEnd])
	blocksString = strings.TrimSuffix(blocksString, "G")
	blocks, _ := parseUint(blocksString)
	return blocks
}

func readPartitions(path string) (res []testPartition) {
	partedRes, _, _ := cmd("sudo", "parted", "-s", path, "unit", "b", "print")
	lines := strings.Split(partedRes, "\n")
	var numStart, numFinish, startStart, startFinish, endStart, endFinish int
	var startParse = false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "Number") {
			numStart = 0
			numFinish = strings.Index(line, "Start")
			startStart = strings.Index(line, "Start")
			startFinish = strings.Index(line, "End")
			endStart = strings.Index(line, "End")
			endFinish = strings.Index(line, "Size")
			startParse = true
			continue
		}
		if !startParse {
			continue
		}
		var partition testPartition
		partition.Number, _ = parseUint(strings.TrimSpace(line[numStart:numFinish]))
		if partition.Number == 0 {
			continue
		}
		partition.Start, _ = parseUint(strings.TrimSuffix(strings.TrimSpace(line[startStart:startFinish]), "B"))
		partition.Last, _ = parseUint(strings.TrimSuffix(strings.TrimSpace(line[endStart:endFinish]), "B"))

		res = append(res, partition)
	}
	return
}

func s(n uint64) string {
	return strconv.FormatUint(n, 10)
}

func sudo(command string, args ...string) (res string, errString string, err error) {
	args = append([]string{command}, args...)
	return cmd("sudo", args...)
}

func TestExt4PartitionMSDOS(t *testing.T) {
	disk, err := createTmpDevice("msdos")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(MSDOS_START_BYTE), s(MSDOS_START_BYTE+GB)) // 1Gb
	part := disk + "p1"
	sudo("mkfs.ext4", part)
	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}

	sudo("mount", part, TMP_MOUNT_DIR)
	defer sudo("umount", part)
	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}
	call(TMP_MOUNT_DIR, "--do")
	res, _, _ := cmd("df", "-BG", part)
	resLines := strings.Split(res, "\n")
	blocksStart := strings.Index(resLines[0], "1G-blocks")
	blocksEnd := blocksStart + len("1G-blocks")
	blocksString := strings.TrimSpace(resLines[1][blocksStart:blocksEnd])
	blocksString = strings.TrimSuffix(blocksString, "G")
	blocks, _ := parseUint(blocksString)
	if blocks != 99 {
		t.Error(resLines[1])
	}

	needPartitions := []testPartition{
		{1, MSDOS_START_BYTE, MSDOS_LAST_BYTE},
	}

	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
	}

	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}
}

func TestExt4PartitionGPT(t *testing.T) {
	disk, err := createTmpDevice("gpt")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(GPT_START_BYTE), s(GPT_START_BYTE+GB)) // 1Gb
	part := disk + "p1"
	sudo("mkfs.ext4", part)
	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}

	sudo("mount", part, TMP_MOUNT_DIR)
	defer sudo("umount", part)
	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}
	call(TMP_MOUNT_DIR, "--do")
	res, _, _ := cmd("df", "-BG", part)
	resLines := strings.Split(res, "\n")
	blocksStart := strings.Index(resLines[0], "1G-blocks")
	blocksEnd := blocksStart + len("1G-blocks")
	blocksString := strings.TrimSpace(resLines[1][blocksStart:blocksEnd])
	blocksString = strings.TrimSuffix(blocksString, "G")
	blocks, _ := parseUint(blocksString)
	if blocks != 99 {
		t.Error(resLines[1])
	}

	needPartitions := []testPartition{
		{1, GPT_START_BYTE, GPT_LAST_BYTE},
	}
	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
	}

	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}

}

func TestXfsPartitionMSDOS(t *testing.T) {
	disk, err := createTmpDevice("msdos")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(MSDOS_START_BYTE), s(MSDOS_START_BYTE+GB)) // 1Gb
	part := disk + "p1"
	sudo("mkfs.xfs", part)
	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}

	sudo("mount", part, TMP_MOUNT_DIR)
	defer sudo("umount", part)
	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}
	call(TMP_MOUNT_DIR, "--do")
	res, _, _ := cmd("df", "-BG", part)
	resLines := strings.Split(res, "\n")
	blocksStart := strings.Index(resLines[0], "1G-blocks")
	blocksEnd := blocksStart + len("1G-blocks")
	blocksString := strings.TrimSpace(resLines[1][blocksStart:blocksEnd])
	blocksString = strings.TrimSuffix(blocksString, "G")
	blocks, _ := parseUint(blocksString)
	if blocks != 100 {
		t.Error(resLines[1])
	}

	needPartitions := []testPartition{
		{1, MSDOS_START_BYTE, MSDOS_LAST_BYTE},
	}
	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
	}
	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}
}

func TestXfsPartitionGPT(t *testing.T) {
	disk, err := createTmpDevice("gpt")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(GPT_START_BYTE), s(GPT_START_BYTE+GB)) // 1Gb
	part := disk + "p1"
	sudo("mkfs.xfs", part)
	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}

	sudo("mount", part, TMP_MOUNT_DIR)
	defer sudo("umount", part)
	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}
	call(TMP_MOUNT_DIR, "--do")
	res, _, _ := cmd("df", "-BG", part)
	resLines := strings.Split(res, "\n")
	blocksStart := strings.Index(resLines[0], "1G-blocks")
	blocksEnd := blocksStart + len("1G-blocks")
	blocksString := strings.TrimSpace(resLines[1][blocksStart:blocksEnd])
	blocksString = strings.TrimSuffix(blocksString, "G")
	blocks, _ := parseUint(blocksString)
	if blocks != 100 {
		t.Errorf("%v[%v:%v]\n'%v'", resLines[1], blocksStart, blocksEnd, blocksString)
	}

	needPartitions := []testPartition{
		{1, GPT_START_BYTE, GPT_LAST_BYTE},
	}

	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
	}
	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}
}

func TestLVMPartitionMSDOS(t *testing.T) {
	disk, err := createTmpDevice("msdos")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(MSDOS_START_BYTE), s(MSDOS_START_BYTE+GB)) // 1Gb
	sudo("parted", "-s", disk, "set", "1", "lvm", "on")

	part := disk + "p1"
	sudo("pvcreate", part)
	sudo("vgcreate", LVM_VG_NAME, part)
	defer sudo("vgremove", "-f", LVM_VG_NAME)
	sudo("lvcreate", "-L", "500M", "-n", LVM_LV_NAME, LVM_VG_NAME)
	lvmLV := filepath.Join("/dev", LVM_VG_NAME, LVM_LV_NAME)
	defer sudo("lvremove", "-f", lvmLV)

	sudo("mkfs.xfs", lvmLV)

	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}
	sudo("mount", lvmLV, TMP_MOUNT_DIR)
	defer sudo("umount", lvmLV)

	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}

	call(TMP_MOUNT_DIR, "--do")
	if 100 != df(TMP_MOUNT_DIR) {
		t.Error("Filesystem size")
	}

	needPartitions := []testPartition{
		{1, 32256, MSDOS_LAST_BYTE},
	}
	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
	}

	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}
}

func TestLVMPartitionGPT(t *testing.T) {
	disk, err := createTmpDevice("gpt")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(GPT_START_BYTE), s(GPT_START_BYTE+GB)) // 1Gb
	sudo("parted", "-s", disk, "set", "1", "lvm", "on")

	part := disk + "p1"
	sudo("pvcreate", part)
	sudo("vgcreate", LVM_VG_NAME, part)
	defer sudo("vgremove", "-f", LVM_VG_NAME)
	sudo("lvcreate", "-L", "500M", "-n", LVM_LV_NAME, LVM_VG_NAME)
	lvmLV := filepath.Join("/dev", LVM_VG_NAME, LVM_LV_NAME)
	defer sudo("lvremove", "-f", lvmLV)

	sudo("mkfs.xfs", lvmLV)

	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}
	sudo("mount", lvmLV, TMP_MOUNT_DIR)
	defer sudo("umount", lvmLV)

	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}

	call(TMP_MOUNT_DIR, "--do")
	if 100 != df(TMP_MOUNT_DIR) {
		t.Error("Filesystem size")
	}

	needPartitions := []testPartition{
		{1, GPT_START_BYTE, GPT_LAST_BYTE},
	}
	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
	}

	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}
}

func TestLVMPartitionInMiddleDiskMSDOS(t *testing.T) {
	disk, err := createTmpDevice("msdos")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(5*GB), s(6*GB))
	sudo("parted", "-s", disk, "set", "1", "lvm", "on")

	part := disk + "p1"
	sudo("pvcreate", part)
	sudo("vgcreate", LVM_VG_NAME, part)
	defer sudo("vgremove", "-f", LVM_VG_NAME)
	sudo("lvcreate", "-L", "500M", "-n", LVM_LV_NAME, LVM_VG_NAME)
	lvmLV := filepath.Join("/dev", LVM_VG_NAME, LVM_LV_NAME)
	defer sudo("lvremove", "-f", lvmLV)

	sudo("mkfs.xfs", lvmLV)

	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}
	sudo("mount", lvmLV, TMP_MOUNT_DIR)
	defer sudo("umount", lvmLV)

	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}

	call(TMP_MOUNT_DIR, "--do")
	if 100 != df(TMP_MOUNT_DIR) {
		t.Error("Filesystem size", df(TMP_MOUNT_DIR))
	}

	needPartitions := []testPartition{
		{2, MSDOS_START_BYTE, 5*GB - 1},
		{1, 5 * GB, MSDOS_LAST_BYTE},
	}
	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
		pretty.Println(readPartitions(disk))
	}

	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}
}

func TestLVMPartitionInMiddleDiskGPT(t *testing.T) {
	disk, err := createTmpDevice("gpt")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(5*GB), s(6*GB))
	sudo("parted", "-s", disk, "set", "1", "lvm", "on")

	part := disk + "p1"
	sudo("pvcreate", part)
	sudo("vgcreate", LVM_VG_NAME, part)
	defer sudo("vgremove", "-f", LVM_VG_NAME)
	sudo("lvcreate", "-L", "500M", "-n", LVM_LV_NAME, LVM_VG_NAME)
	lvmLV := filepath.Join("/dev", LVM_VG_NAME, LVM_LV_NAME)
	defer sudo("lvremove", "-f", lvmLV)

	sudo("mkfs.xfs", lvmLV)

	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}
	sudo("mount", lvmLV, TMP_MOUNT_DIR)
	defer sudo("umount", lvmLV)

	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}

	call(TMP_MOUNT_DIR, "--do")
	if 100 != df(TMP_MOUNT_DIR) {
		t.Error("Filesystem size", df(TMP_MOUNT_DIR))
	}

	needPartitions := []testPartition{
		{2, GPT_START_BYTE, 5*GB - 1},
		{1, 5 * GB, GPT_LAST_BYTE},
	}
	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
		pretty.Println(readPartitions(disk))
	}

	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}
}

func TestLVMPartitionIn2MiddleDiskMSDOS(t *testing.T) {
	disk, err := createTmpDevice("msdos")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(5*GB), s(6*GB-1))
	sudo("parted", "-s", disk, "set", "1", "lvm", "on")

	// No create lvm on second partition. Partition for split free space
	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(10*GB), s(11*GB-1))

	part := disk + "p1"
	sudo("pvcreate", part)
	sudo("vgcreate", LVM_VG_NAME, part)
	defer sudo("vgremove", "-f", LVM_VG_NAME)
	sudo("lvcreate", "-L", "500M", "-n", LVM_LV_NAME, LVM_VG_NAME)
	lvmLV := filepath.Join("/dev", LVM_VG_NAME, LVM_LV_NAME)
	defer sudo("lvremove", "-f", lvmLV)

	sudo("mkfs.xfs", lvmLV)

	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}
	sudo("mount", lvmLV, TMP_MOUNT_DIR)
	defer sudo("umount", lvmLV)

	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}

	call(TMP_MOUNT_DIR, "--do")
	if 99 != df(TMP_MOUNT_DIR) {
		t.Error("Filesystem size", df(TMP_MOUNT_DIR))
	}

	needPartitions := []testPartition{
		{3, MSDOS_START_BYTE, 5*GB - 1},
		{1, 5 * GB, 10*GB - 1},
		{2, 10 * GB, 11*GB - 1},
		{4, 11 * GB, MSDOS_LAST_BYTE},
	}
	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
		pretty.Println(readPartitions(disk))
	}

	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}
}

func TestLVMPartitionIn2MiddleDiskGPT(t *testing.T) {
	disk, err := createTmpDevice("gpt")
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTmpDevice(disk)

	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(5*GB), s(6*GB-1))
	sudo("parted", "-s", disk, "set", "1", "lvm", "on")

	// No create lvm on second partition. Partition for split free space
	sudo("parted", "-s", disk, "unit", "b", "mkpart", "primary", s(10*GB), s(11*GB-1))

	part := disk + "p1"
	sudo("pvcreate", part)
	sudo("vgcreate", LVM_VG_NAME, part)
	defer sudo("vgremove", "-f", LVM_VG_NAME)
	sudo("lvcreate", "-L", "500M", "-n", LVM_LV_NAME, LVM_VG_NAME)
	lvmLV := filepath.Join("/dev", LVM_VG_NAME, LVM_LV_NAME)
	defer sudo("lvremove", "-f", lvmLV)

	sudo("mkfs.xfs", lvmLV)

	err = os.MkdirAll(TMP_MOUNT_DIR, 0700)
	if err == nil {
		defer os.Remove(TMP_MOUNT_DIR)
	} else {
		t.Fatal(err)
	}
	sudo("mount", lvmLV, TMP_MOUNT_DIR)
	defer sudo("umount", lvmLV)

	sudo("chmod", "a+rwx", TMP_MOUNT_DIR)
	err = ioutil.WriteFile(filepath.Join(TMP_MOUNT_DIR, "test"), []byte("OK"), 0666)
	if err != nil {
		t.Error("Can't write test file", err)
	}

	call(TMP_MOUNT_DIR, "--do")
	if 99 != df(TMP_MOUNT_DIR) {
		t.Error("Filesystem size", df(TMP_MOUNT_DIR))
	}

	needPartitions := []testPartition{
		{3, GPT_START_BYTE, 5*GB - 1},
		{1, 5 * GB, 10*GB - 1},
		{2, 10 * GB, 11*GB - 1},
		{4, 11 * GB, GPT_LAST_BYTE},
	}
	partDiff := pretty.Diff(readPartitions(disk), needPartitions)
	if partDiff != nil {
		t.Error(partDiff)
		pretty.Println(readPartitions(disk))
	}

	testBytes, err := ioutil.ReadFile(filepath.Join(TMP_MOUNT_DIR, "test"))
	if err != nil {
		t.Error("Can't read test file", err)
	}
	if string(testBytes) != "OK" {
		t.Error("Bad file content:", string(testBytes))
	}
}
