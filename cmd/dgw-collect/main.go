// Command dgw-collect 是知识采集器（ADR-0007）的可执行入口：结构知识
// 自动采集（migration 文件为主干 + GORM 模型交叉验证 + 按需生产校准），
// 输出语义作者入口 YAML 草稿（与 07 同步管线编译校验兼容）。
//
// 子命令：
//
//	dgw-collect scan --repo ROOT --manifest FILE [--service NAME]
//	                 [--out DIR] [--no-gorm]
//	      采集清单内服务：迁移解析 → GORM 交叉验证 → 草稿写出 →
//	      全量编译兼容检查（第三道闸）
//	dgw-collect calibrate --repo ROOT --manifest FILE --service NAME
//	                      --dsn URL
//	      连只读从库做生产校准（漂移报告，只报告不改；v1 低优先）
//
// 触发 = 手动 on-demand（无轮询/定时，ADR-0007）。采集草稿经人工
// review（PR）合入语义仓库后再跑 dgw semantic-sync 进运行时。
//
// 退出码：0 = 采集成功且门禁全过；2 = 采集完成但有 error 级发现
// （GORM 交叉验证/迁移解析/编译兼容失败——草稿照写，交给人确认）；
// 1 = 操作失败（参数/目录/连接错误，未产出）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/yuefanxiao/DataIntelligent/internal/collector"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("dgw-collect: ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "scan":
		os.Exit(cmdScan())
	case "calibrate":
		os.Exit(cmdCalibrate())
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "dgw-collect: 未知子命令 %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dgw-collect — 知识采集器（结构自动 / 语义人工，ADR-0007）

用法：
  dgw-collect scan --repo ROOT --manifest FILE [--service NAME] [--out DIR] [--no-gorm]
      采集清单内服务 → 结构 YAML 草稿（services/<service>.yaml）
  dgw-collect calibrate --repo ROOT --manifest FILE --service NAME --dsn URL
      连只读从库校准（information_schema 对照，漂移报告，只报告不改）

参数：
  --repo ROOT      服务仓库根目录（monorepo root，如 neo-cloud）
  --manifest FILE  采集清单（服务 → 目录/生产库名映射，见 samples/collector/manifest.yaml）
  --service NAME   只采集清单里一个服务（scan/calibrate 都用）
  --out DIR        scan 草稿输出目录（services/ 子目录，缺省 ./collect-out）
  --no-gorm        跳过 GORM 交叉验证（第二道闸）
  --dsn URL        calibrate 的只读从库连接串（postgres://user:pass@host/db）

工作流：跑采集 → 生成 YAML 草稿 → 人工 review（PR）→ 合入语义仓库
→ dgw semantic-sync。校准是 v1 低优先的兜底（migration 文件非绝对
真相，手工 DDL 先例存在）。

退出码：0 全过 / 2 有 error 级发现（草稿照写，交人确认）/ 1 操作失败
（参数/目录/连接错误）。

注意：--service 增量采集时，第三道闸（编译兼容）校验的是 out 目录里
现有全部草稿（含早前采集的），服务从清单移除后请清理 out 目录或重跑
全量采集。
`)
}

// cmdScan 跑 scan；返回进程退出码。
func cmdScan() int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	repo := fs.String("repo", "", "服务仓库根目录")
	manifest := fs.String("manifest", "", "采集清单路径")
	service := fs.String("service", "", "只采集清单里一个服务")
	out := fs.String("out", "./collect-out", "草稿输出目录")
	noGorm := fs.Bool("no-gorm", false, "跳过 GORM 交叉验证")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(os.Args[2:]); err != nil {
		return 1 // flag 用法错误 = 操作失败（1），与「2 = 门禁发现」区分
	}

	if *repo == "" || *manifest == "" {
		log.Print("scan 需要 --repo 与 --manifest")
		return 1
	}
	m, err := collector.LoadManifest(*manifest)
	if err != nil {
		log.Fatalf("加载采集清单失败: %v", err)
	}
	res, err := collector.Collect(collector.CollectConfig{
		Repo:     *repo,
		Manifest: m,
		Service:  *service,
		GORM:     !*noGorm,
		OutDir:   *out,
	})
	if err != nil {
		log.Fatalf("采集失败（未产出）: %v", err)
	}

	gateErrors := 0
	for _, sr := range res.Services {
		fmt.Printf("dgw: 采集 %s（库 %s%s）：%d 表 / %d 列 / %d 枚举 / %d 引用\n",
			sr.Name, sr.DB, schemaSuffix(sr.Schema), sr.Tables, sr.Columns, sr.Enums, sr.Refs)
		n := sr.PrintFindings()
		gateErrors += n
	}
	if res.CompileErr != nil {
		fmt.Printf("dgw: 编译兼容检查（第三道闸）: 失败 — %v\n", res.CompileErr)
		gateErrors++
	} else {
		fmt.Printf("dgw: 编译兼容检查: 通过（产出可进同步管线）\n")
	}
	fmt.Printf("dgw: 草稿已写出到 %s/services/（人工 review 后入语义仓库）\n", *out)
	if gateErrors > 0 {
		fmt.Printf("dgw: 发现 %d 个 error 级门禁问题（草稿照写，确认后合入）\n", gateErrors)
		return 2
	}
	fmt.Printf("dgw: 采集完成，门禁全过\n")
	return 0
}

// cmdCalibrate 连只读从库做生产校准；返回进程退出码。
func cmdCalibrate() int {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	repo := fs.String("repo", "", "服务仓库根目录")
	manifest := fs.String("manifest", "", "采集清单路径")
	service := fs.String("service", "", "要校准的服务（必填，一次一个）")
	dsn := fs.String("dsn", os.Getenv("DGW_COLLECT_DSN"), "只读从库连接串（可经 DGW_COLLECT_DSN env 传入，避免 argv 泄露口令）")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(os.Args[2:]); err != nil {
		return 1
	}

	if *repo == "" || *manifest == "" || *service == "" || *dsn == "" {
		log.Print("calibrate 需要 --repo --manifest --service --dsn（DSN 也可经 DGW_COLLECT_DSN 环境变量传入）")
		return 1
	}
	m, err := collector.LoadManifest(*manifest)
	if err != nil {
		log.Fatalf("加载采集清单失败: %v", err)
	}
	ms, err := m.Find(*service)
	if err != nil {
		log.Fatalf("%v", err)
	}
	st, findings, err := collector.ParseServiceMigrations(ms, *repo)
	if err != nil {
		log.Printf("%v", err)
		return 1
	}
	migrationErrors := 0
	for _, f := range findings {
		fmt.Printf("  %s\n", f.String())
		if f.Severity == collector.SeverityError {
			migrationErrors++
		}
	}
	if len(st.Tables) == 0 {
		log.Print("迁移解析没有产出任何表，无法校准")
		return 1
	}
	ctx := context.Background()
	calFindings, err := collector.Calibrate(ctx, *dsn, st)
	if err != nil {
		log.Fatalf("校准失败: %v", err)
	}
	fmt.Printf("dgw: 校准 %s（库 %s）：%d 表\n", ms.Name, ms.DB, len(st.Tables))
	for _, f := range calFindings {
		fmt.Printf("  %s\n", f.String())
	}
	errs := 0
	warns := 0
	for _, f := range calFindings {
		switch f.Severity {
		case collector.SeverityError:
			errs++
		case collector.SeverityWarn:
			warns++
		}
	}
	fmt.Printf("dgw: 校准完成（漂移报告只报告不改）：error %d / warn %d\n", errs, warns)
	// 迁移解析的 error 级发现（不可解析文件）与校准发现同门禁
	// （与 scan 的门禁语义一致）。
	if errs+migrationErrors > 0 {
		return 2
	}
	return 0
}

func schemaSuffix(s string) string {
	if s == "" || s == "public" {
		return "（public）"
	}
	return "（schema " + s + "）"
}
