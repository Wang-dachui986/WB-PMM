"""生成 NSIS 安装/卸载清单（package.ps1 清单段的 Python 移植，供无 PowerShell 的 macOS/Linux 使用）。

用法：python3 -I windows/gen-payload-manifest.py <发布目录> <输出 .nsh>

规则与 package.ps1 一致：必需文件缺失、重解析点（符号链接）、无法安全写入 NSIS 的文件名、
发布目录自带 Uninstall.exe 都会拒绝。差异仅在排序：这里按字节序（大写在前），与历次 macOS
交叉打包产物一致；长度相同的目录删除先后不定（它们互不嵌套，先后无影响），长度降序保证
子目录先于父目录删除。另外拒绝 macOS 的 ._* / .DS_Store 伴生文件，防止误打进安装包。
"""
import os
import re
import sys

REQUIRED = ("WB-PMM.exe", "WB-PMM.runtimeconfig.json", "hostfxr.dll", "Helpers/wbgui.exe", "Helpers/wb2api.exe")


def collect(app):
    files, dirs = [], []
    for root, dnames, fnames in os.walk(app):
        for name in dnames + fnames:
            full = os.path.join(root, name)
            if name.startswith("._") or name == ".DS_Store":
                sys.exit(f"发布目录包含 macOS 伴生文件：{full}")
            if os.path.islink(full):
                sys.exit("发布目录包含重解析点")
            rel = os.path.relpath(full, app).replace("/", "\\")
            if re.search(r'[\$"\r\n]', rel):
                sys.exit("发布文件名无法安全写入 NSIS 清单")
            (dirs if os.path.isdir(full) else files).append(rel)
    if "Uninstall.exe" in files:
        sys.exit("发布目录不得包含卸载程序")
    return sorted(files), sorted(dirs)


def render(files, dirs):
    lines = ["!macro GuardPayloadDirectories prefix"]
    for i, d in enumerate(dirs):
        done = "${prefix}Guard%dDone" % i
        lines += [
            "  System::Call 'kernel32::GetFileAttributesW(w \"$INSTDIR\\%s\") i .r0'" % d,
            '  StrCmp $0 "-1" ' + done,
            "  IntOp $1 $0 & 0x400",
            '  StrCmp $1 "0" ' + done,
            '  MessageBox MB_ICONSTOP "安装目录包含重解析点，已停止以保护其他文件。"',
            "  Abort",
            done + ":",
        ]
    lines += ["!macroend", "!macro RemovePayloadFiles"]
    for i, f in enumerate(files):
        done = "PayloadFile%dDone" % i
        lines += [
            '  Delete "$INSTDIR\\%s"' % f,
            '  IfFileExists "$INSTDIR\\%s" 0 %s' % (f, done),
            '  MessageBox MB_ICONSTOP "安装文件被占用，无法安全卸载。"',
            "  Abort",
            done + ":",
        ]
    lines += [
        '  Delete "$INSTDIR\\Uninstall.exe"',
        '  IfFileExists "$INSTDIR\\Uninstall.exe" 0 PayloadUninstallerDone',
        '  MessageBox MB_ICONSTOP "卸载程序被占用，无法安全卸载。"',
        "  Abort",
        "PayloadUninstallerDone:",
    ]
    for d in sorted(dirs, key=len, reverse=True):
        lines.append('  RMDir "$INSTDIR\\%s"' % d)
    lines += ['  RMDir "$INSTDIR"', "!macroend"]
    return "\n".join(lines) + "\n"


def main():
    if len(sys.argv) != 3:
        sys.exit("用法：gen-payload-manifest.py <发布目录> <输出 .nsh>")
    app, out = sys.argv[1], sys.argv[2]
    for rel in REQUIRED:
        if not os.path.isfile(os.path.join(app, rel)):
            sys.exit(f"发布目录缺少 {rel}")
    files, dirs = collect(app)
    with open(out, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(render(files, dirs))
    print(f"files={len(files)} dirs={len(dirs)} -> {out}")


if __name__ == "__main__":
    main()
