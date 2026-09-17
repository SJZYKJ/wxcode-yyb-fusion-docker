# 青龙修复脚本

这个目录收录了对 `SuperNaiBA/YYB-GO-Script` 中已确认报错脚本的最小修复版，用于 YYB Go 多账号调用。

## 已修复

- `DDYX.py`、`DSMMHYSCQD.py`、`DSTX.py`、`DTSH.py`、`JTC.py`、`JYXEJYFHS.py`、`LDXQ.py`、`NWDJG.py`、`NXDC.py`、`QC.py`、`SANF.py`、`THYC.py`、`XFJ.py`、`byd_sign.py`：修复 `YYB_SERVER` 配置提示代码的缩进错误。
- `WRN.py`：补充实际运行所需的 `sys` 导入。
- `MS.js`：兼容会员信息的新旧返回结构，缺少 `memberId` 时停止当前账号，避免连续异常。
- `jyk.py`：正确解析 `地址@账号标识` 格式，避免把账号 ID 当作 YYB 服务地址。
- `TCLXLC.js`：移除登录流程中对未定义 `parsedServer` 变量的引用。

## 使用

将需要的文件覆盖到青龙订阅目录中对应的脚本，然后先做语法检查：

```bash
python3 -m py_compile /ql/data/scripts/SuperNaiBA_YYB-GO-Script/脚本名.py
node --check /ql/data/scripts/SuperNaiBA_YYB-GO-Script/脚本名.js
```

重新执行上游订阅可能会覆盖这些修复，建议在订阅更新后重新检查。脚本仍受原项目授权和条款约束。
