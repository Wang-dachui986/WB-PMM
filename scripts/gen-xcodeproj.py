#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""生成 macos/WorkBuddyDesktop.xcodeproj（project.pbxproj + 共享 scheme）。

为什么用生成器而不是手写 pbxproj：
  - 对象 ID 由路径 sha1 派生，重复运行结果完全一致（幂等，可 diff）
  - 新增源文件只需改 SOURCES 列表，不用手工维护 buildFile/fileRef/group 三处引用
  - 避免手写时漏挂 PBXSourcesBuildPhase（表现是「文件在工程里但没被编译」，很难查）

运行：python3 scripts/gen-xcodeproj.py
"""
from __future__ import annotations

import hashlib
import argparse
import re
import sys
from pathlib import Path

parser = argparse.ArgumentParser(description="Synchronize Swift sources without replacing project customizations")
parser.add_argument("--macos", type=Path, default=Path(__file__).resolve().parents[1] / "macos")
MACOS = parser.parse_args().macos.resolve()
PROJECT_NAME = "WorkBuddyDesktop"
APP_PRODUCT_NAME = "WB-PMM"
APP_ICON_FILE = "WBPMM.icns"
MENU_ICON_FILE = "WBPMMMenuTemplate.png"
BUNDLE_ID = "cn.workbuddy.desktop"
DEPLOYMENT_TARGET = "14.0"
SWIFT_VERSION = "6.0"
MARKETING_VERSION = "0.1.0"
BUILD_NUMBER = "1"

# 分组 → 源文件（相对 macos/WorkBuddyDesktop/）
GROUPS: dict[str, list[str]] = {
    "App": [
        "WorkBuddyDesktopApp.swift",
        "MainWindowView.swift",
        "MenuBarView.swift",
    ],
    "Core": [
        "AppPaths.swift",
        "AppLog.swift",
        "InstanceLock.swift",
        "ChildEnvironment.swift",
        "AppSettings.swift",
        "DesktopUtilities.swift",
    ],
    "Services": [
        "GatewayState.swift",
        "PanelHandshake.swift",
        "PortInspector.swift",
        "ManagedProcess.swift",
        "GatewayConfigFile.swift",
        "ServiceManager.swift",
        "PanelSessionClient.swift",
    ],
    "Web": ["PanelWebView.swift"],
    "Bridge": ["NativeBridge.swift"],
}

# 把两个 Go helper 拷进 Contents/Helpers/ 的构建阶段
HELPERS_SCRIPT = """set -eu
# 把两个 Go helper 拷进 Contents/Helpers/。
# 源目录由 WB_HELPERS_SRC 指定，默认取 scripts/build.sh 的输出目录。
SRC="${WB_HELPERS_SRC:-$HOME/workbuddy2api-build/bin}"
DEST="$TARGET_BUILD_DIR/$CONTENTS_FOLDER_PATH/Helpers"

# 打包前断言：面板必须支持 --desktop。
# 应用是按「wbgui --desktop 可用」这个契约打包的；打进一个不认识该标志的旧二进制，
# 结果会是应用启动后静默等 10 秒再报「面板未就绪」，原因极难看出来。
# 这一步把该错误从运行期提前到构建期。
if [ -x "$SRC/wbgui" ] && ! "$SRC/wbgui" --help 2>&1 | grep -q -- '-desktop'; then
  echo "error: $SRC/wbgui 不支持 --desktop —— 它是面板补丁之前的旧产物。" >&2
  echo "note: 请重新运行 scripts/build.sh 构建面板后重试。" >&2
  exit 1
fi

mkdir -p "$DEST"
for name in wbgui wb2api; do
  if [ ! -x "$SRC/$name" ]; then
    echo "error: 找不到 helper：$SRC/$name" >&2
    echo "note: 请先运行 scripts/build.sh，或设置 WB_HELPERS_SRC" >&2
    exit 1
  fi
  cp -f "$SRC/$name" "$DEST/$name"
  chmod 0755 "$DEST/$name"
done
# 兜底清除 AppleDouble 伴生文件（打包禁止携带，见开发计划 §17.3）
find "$DEST" -name '._*' -delete 2>/dev/null || true
echo "helpers 已就位：$DEST"
"""


def oid(key: str) -> str:
    """由键派生稳定的 24 位十六进制对象 ID。"""
    return hashlib.sha1(key.encode("utf-8")).hexdigest()[:24].upper()


def esc(s: str) -> str:
    """pbxproj 引号字符串转义。"""
    return s.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def sync_existing(project: Path) -> None:
    """Sync sources, icon resources, and app product name while retaining the target and scheme."""
    text = project.read_text(encoding="utf-8")
    for group, names in GROUPS.items():
        for name in names:
            ref_match = re.search(r"([A-F0-9]{24}) /\* " + re.escape(name) + r" \*/ = \{isa = PBXFileReference;", text)
            ref = ref_match.group(1) if ref_match else oid(f"file:{group}/{name}")
            if not ref_match:
                line = f'\t\t{ref} /* {name} */ = {{isa = PBXFileReference; lastKnownFileType = sourcecode.swift; path = {name}; sourceTree = "<group>"; }};\n'
                text = text.replace("/* End PBXFileReference section */", line + "/* End PBXFileReference section */")
                pattern = r"(" + oid(f"group:{group}") + r" /\* " + re.escape(group) + r" \*/ = \{.*?children = \(\n)"
                text, count = re.subn(pattern, lambda m: m.group(1) + f"\t\t\t\t{ref} /* {name} */,\n", text, count=1, flags=re.S)
                if count != 1: raise ValueError(f"Existing group missing: {group}; review manually")
            build_match = re.search(r"([A-F0-9]{24}) /\* " + re.escape(name) + r" in Sources \*/ = \{isa = PBXBuildFile;", text)
            build = build_match.group(1) if build_match else oid(f"buildFile:{group}/{name}")
            if not build_match:
                line = f"\t\t{build} /* {name} in Sources */ = {{isa = PBXBuildFile; fileRef = {ref} /* {name} */; }};\n"
                text = text.replace("/* End PBXBuildFile section */", line + "/* End PBXBuildFile section */")
            phase = text.split("/* Begin PBXSourcesBuildPhase section */", 1)[1].split("/* End PBXSourcesBuildPhase section */", 1)[0]
            if not re.search(r"\b" + build + r" /\* .*? \*/,", phase):
                pattern = r"(/\* Begin PBXSourcesBuildPhase section \*/.*?files = \(\n)"
                text, count = re.subn(pattern, lambda m: m.group(1) + f"\t\t\t\t{build} /* {name} in Sources */,\n", text, count=1, flags=re.S)
                if count != 1: raise ValueError("Sources phase missing; review manually")

    source_group = oid(f"group:{PROJECT_NAME}")
    for icon_file, file_type in ((APP_ICON_FILE, "image.icns"), (MENU_ICON_FILE, "image.png")):
        icon_ref = oid(f"file:Assets/{icon_file}")
        icon_build = oid(f"buildFile:Assets/{icon_file}")
        if not re.search(rf"{icon_ref} /\* {icon_file} \*/ = \{{isa = PBXFileReference;", text):
            line = (f'\t\t{icon_ref} /* {icon_file} */ = {{isa = PBXFileReference; '
                    f'lastKnownFileType = {file_type}; path = Assets/{icon_file}; sourceTree = "<group>"; }};\n')
            text = text.replace("/* End PBXFileReference section */", line + "/* End PBXFileReference section */")
        group = text.split(f"{source_group} /* {PROJECT_NAME} */ = {{", 1)[1].split("\t\t};", 1)[0]
        if f"{icon_ref} /* {icon_file} */" not in group:
            pattern = rf"({source_group} /\* {PROJECT_NAME} \*/ = \{{.*?children = \(\n)"
            text, count = re.subn(pattern, lambda m: m.group(1) + f"\t\t\t\t{icon_ref} /* {icon_file} */,\n", text, count=1, flags=re.S)
            if count != 1: raise ValueError("App source group missing; review manually")
        if not re.search(rf"{icon_build} /\* {icon_file} in Resources \*/ = \{{isa = PBXBuildFile;", text):
            line = (f"\t\t{icon_build} /* {icon_file} in Resources */ = {{isa = PBXBuildFile; "
                    f"fileRef = {icon_ref} /* {icon_file} */; }};\n")
            text = text.replace("/* End PBXBuildFile section */", line + "/* End PBXBuildFile section */")
        resources = text.split("/* Begin PBXResourcesBuildPhase section */", 1)[1].split("/* End PBXResourcesBuildPhase section */", 1)[0]
        if f"{icon_build} /* {icon_file} in Resources */" not in resources:
            pattern = r"(/\* Begin PBXResourcesBuildPhase section \*/.*?files = \(\n)"
            text, count = re.subn(pattern, lambda m: m.group(1) + f"\t\t\t\t{icon_build} /* {icon_file} in Resources */,\n", text, count=1, flags=re.S)
            if count != 1: raise ValueError("Resources phase missing; review manually")

    product_ref = oid("product:app")
    text = text.replace(f"{product_ref} /* {PROJECT_NAME}.app */", f"{product_ref} /* {APP_PRODUCT_NAME}.app */")
    text = text.replace(f"path = {PROJECT_NAME}.app;", f'path = "{APP_PRODUCT_NAME}.app";')
    text = text.replace(f"productName = {PROJECT_NAME};", f'productName = "{APP_PRODUCT_NAME}";')
    text = text.replace(f"PRODUCT_NAME = {PROJECT_NAME};", f'PRODUCT_NAME = "{APP_PRODUCT_NAME}";')
    if f"EXECUTABLE_NAME = {PROJECT_NAME};" not in text:
        text = text.replace("\t\t\t\tGENERATE_INFOPLIST_FILE = NO;",
                            f"\t\t\t\tEXECUTABLE_NAME = {PROJECT_NAME};\n\t\t\t\tGENERATE_INFOPLIST_FILE = NO;")
    if text != project.read_text(encoding="utf-8"): project.write_text(text, encoding="utf-8")
    scheme = project.parent / "xcshareddata" / "xcschemes" / f"{PROJECT_NAME}.xcscheme"
    if scheme.exists():
        old = scheme.read_text(encoding="utf-8")
        new = old.replace(f'BuildableName = "{PROJECT_NAME}.app"', f'BuildableName = "{APP_PRODUCT_NAME}.app"')
        if new != old: scheme.write_text(new, encoding="utf-8")
    print(f"Synchronized {sum(map(len, GROUPS.values()))} Swift sources, icons, and {APP_PRODUCT_NAME}.app; target and scheme retained")


def main() -> int:
    proj_dir = MACOS / f"{PROJECT_NAME}.xcodeproj"
    # Include current files in known groups, ignoring exFAT resource-fork sidecars.
    for group, names in GROUPS.items():
        actual = sorted(p.name for p in (MACOS / PROJECT_NAME / group).glob("*.swift") if not p.name.startswith("._"))
        missing = set(names) - set(actual)
        if missing: raise ValueError(f"Missing sources in {group}: {sorted(missing)}")
        names.extend(name for name in actual if name not in names)
    if (proj_dir / "project.pbxproj").exists():
        sync_existing(proj_dir / "project.pbxproj")
        return 0

    ids = {
        "project": oid("project"),
        "target": oid("target"),
        "rootGroup": oid("group:root"),
        "productsGroup": oid("group:Products"),
        "srcGroup": oid("group:WorkBuddyDesktop"),
        "product": oid("product:app"),
        "iconFile": oid(f"file:Assets/{APP_ICON_FILE}"),
        "iconBuildFile": oid(f"buildFile:Assets/{APP_ICON_FILE}"),
        "menuIconFile": oid(f"file:Assets/{MENU_ICON_FILE}"),
        "menuIconBuildFile": oid(f"buildFile:Assets/{MENU_ICON_FILE}"),
        "infoPlist": oid("file:Info.plist"),
        "entitlements": oid("file:WorkBuddyDesktop.entitlements"),
        "sourcesPhase": oid("phase:Sources"),
        "frameworksPhase": oid("phase:Frameworks"),
        "resourcesPhase": oid("phase:Resources"),
        "helpersPhase": oid("phase:Helpers"),
        "projConfList": oid("confList:project"),
        "targetConfList": oid("confList:target"),
        "projDebug": oid("conf:project:Debug"),
        "projRelease": oid("conf:project:Release"),
        "targetDebug": oid("conf:target:Debug"),
        "targetRelease": oid("conf:target:Release"),
    }

    # ── PBXBuildFile / PBXFileReference ─────────────────────────────────────
    build_files: list[str] = []
    file_refs: list[str] = []
    group_children: dict[str, list[str]] = {g: [] for g in GROUPS}

    for group, files in GROUPS.items():
        for name in files:
            key = f"{group}/{name}"
            ref = oid(f"file:{key}")
            bf = oid(f"buildFile:{key}")
            build_files.append(
                f"\t\t{bf} /* {name} in Sources */ = {{isa = PBXBuildFile; "
                f"fileRef = {ref} /* {name} */; }};"
            )
            file_refs.append(
                f"\t\t{ref} /* {name} */ = {{isa = PBXFileReference; "
                f"lastKnownFileType = sourcecode.swift; path = {name}; sourceTree = \"<group>\"; }};"
            )
            group_children[group].append(f"\t\t\t\t{ref} /* {name} */,")

    build_files.append(
        f"\t\t{ids['iconBuildFile']} /* {APP_ICON_FILE} in Resources */ = {{isa = PBXBuildFile; "
        f"fileRef = {ids['iconFile']} /* {APP_ICON_FILE} */; }};"
    )
    build_files.append(
        f"\t\t{ids['menuIconBuildFile']} /* {MENU_ICON_FILE} in Resources */ = {{isa = PBXBuildFile; "
        f"fileRef = {ids['menuIconFile']} /* {MENU_ICON_FILE} */; }};"
    )
    file_refs.append(
        f"\t\t{ids['iconFile']} /* {APP_ICON_FILE} */ = {{isa = PBXFileReference; "
        f'lastKnownFileType = image.icns; path = Assets/{APP_ICON_FILE}; sourceTree = "<group>"; }};'
    )
    file_refs.append(
        f"\t\t{ids['menuIconFile']} /* {MENU_ICON_FILE} */ = {{isa = PBXFileReference; "
        f'lastKnownFileType = image.png; path = Assets/{MENU_ICON_FILE}; sourceTree = "<group>"; }};'
    )
    file_refs.append(
        f"\t\t{ids['product']} /* {APP_PRODUCT_NAME}.app */ = {{isa = PBXFileReference; "
        f"explicitFileType = wrapper.application; includeInIndex = 0; "
        f'path = "{APP_PRODUCT_NAME}.app"; sourceTree = BUILT_PRODUCTS_DIR; }};'
    )
    file_refs.append(
        f"\t\t{ids['infoPlist']} /* Info.plist */ = {{isa = PBXFileReference; "
        f"lastKnownFileType = text.plist.xml; path = Info.plist; sourceTree = \"<group>\"; }};"
    )
    file_refs.append(
        f"\t\t{ids['entitlements']} /* {PROJECT_NAME}.entitlements */ = {{isa = PBXFileReference; "
        f"lastKnownFileType = text.plist.entitlements; path = {PROJECT_NAME}.entitlements; "
        f"sourceTree = \"<group>\"; }};"
    )

    # ── PBXGroup ────────────────────────────────────────────────────────────
    groups: list[str] = []
    subgroup_refs: list[str] = []
    for group in GROUPS:
        gid = oid(f"group:{group}")
        subgroup_refs.append(f"\t\t\t\t{gid} /* {group} */,")
        children = "\n".join(group_children[group])
        groups.append(
            f"\t\t{gid} /* {group} */ = {{\n"
            f"\t\t\tisa = PBXGroup;\n"
            f"\t\t\tchildren = (\n{children}\n\t\t\t);\n"
            f"\t\t\tpath = {group};\n"
            f"\t\t\tsourceTree = \"<group>\";\n"
            f"\t\t}};"
        )

    src_children = "\n".join(subgroup_refs + [
        f"\t\t\t\t{ids['iconFile']} /* {APP_ICON_FILE} */,",
        f"\t\t\t\t{ids['menuIconFile']} /* {MENU_ICON_FILE} */,",
        f"\t\t\t\t{ids['infoPlist']} /* Info.plist */,",
        f"\t\t\t\t{ids['entitlements']} /* {PROJECT_NAME}.entitlements */,",
    ])
    groups.append(
        f"\t\t{ids['srcGroup']} /* {PROJECT_NAME} */ = {{\n"
        f"\t\t\tisa = PBXGroup;\n"
        f"\t\t\tchildren = (\n{src_children}\n\t\t\t);\n"
        f"\t\t\tpath = {PROJECT_NAME};\n"
        f"\t\t\tsourceTree = \"<group>\";\n"
        f"\t\t}};"
    )
    groups.append(
        f"\t\t{ids['productsGroup']} /* Products */ = {{\n"
        f"\t\t\tisa = PBXGroup;\n"
        f"\t\t\tchildren = (\n\t\t\t\t{ids['product']} /* {APP_PRODUCT_NAME}.app */,\n\t\t\t);\n"
        f"\t\t\tname = Products;\n"
        f"\t\t\tsourceTree = \"<group>\";\n"
        f"\t\t}};"
    )
    groups.append(
        f"\t\t{ids['rootGroup']} = {{\n"
        f"\t\t\tisa = PBXGroup;\n"
        f"\t\t\tchildren = (\n"
        f"\t\t\t\t{ids['srcGroup']} /* {PROJECT_NAME} */,\n"
        f"\t\t\t\t{ids['productsGroup']} /* Products */,\n"
        f"\t\t\t);\n"
        f"\t\t\tsourceTree = \"<group>\";\n"
        f"\t\t}};"
    )

    # ── 构建阶段 ────────────────────────────────────────────────────────────
    source_file_ids = "\n".join(
        f"\t\t\t\t{oid(f'buildFile:{g}/{n}')} /* {n} in Sources */,"
        for g, files in GROUPS.items() for n in files
    )

    # ── 构建配置 ────────────────────────────────────────────────────────────
    common = [
        "ALWAYS_SEARCH_USER_PATHS = NO;",
        "CLANG_ENABLE_OBJC_WEAK = YES;",
        "COPY_PHASE_STRIP = NO;",
        "ENABLE_STRICT_OBJC_MSGSEND = YES;",
        "GCC_NO_COMMON_BLOCKS = YES;",
        f"MACOSX_DEPLOYMENT_TARGET = {DEPLOYMENT_TARGET};",
        "SDKROOT = macosx;",
        f"SWIFT_VERSION = {SWIFT_VERSION};",
        "WORKBUDDY_BUILD_TIME = unrecorded;",
        # 用户脚本沙盒会挡住「从 ~/workbuddy2api-build 拷 helper」这一步，必须关闭
        "ENABLE_USER_SCRIPT_SANDBOXING = NO;",
        "CLANG_ENABLE_MODULES = YES;",
        "SWIFT_EMIT_LOC_STRINGS = NO;",
    ]
    proj_debug = common + [
        "DEBUG_INFORMATION_FORMAT = dwarf;",
        "ENABLE_TESTABILITY = YES;",
        "GCC_OPTIMIZATION_LEVEL = 0;",
        "GCC_PREPROCESSOR_DEFINITIONS = (\"DEBUG=1\", \"$(inherited)\");",
        "MTL_ENABLE_DEBUG_INFO = INCLUDE_SOURCE;",
        "ONLY_ACTIVE_ARCH = YES;",
        "SWIFT_ACTIVE_COMPILATION_CONDITIONS = DEBUG;",
        "SWIFT_OPTIMIZATION_LEVEL = \"-Onone\";",
    ]
    proj_release = common + [
        "DEBUG_INFORMATION_FORMAT = \"dwarf-with-dsym\";",
        "ENABLE_NS_ASSERTIONS = NO;",
        "MTL_ENABLE_DEBUG_INFO = NO;",
        "SWIFT_COMPILATION_MODE = wholemodule;",
    ]
    target_common = [
        "CODE_SIGN_ENTITLEMENTS = "
        f"{PROJECT_NAME}/{PROJECT_NAME}.entitlements;",
        # 开发阶段用 ad-hoc 签名：本机运行不需要开发者证书
        "CODE_SIGN_IDENTITY = \"-\";",
        "CODE_SIGN_STYLE = Manual;",
        "DEVELOPMENT_TEAM = \"\";",
        # 不启用 Hardened Runtime：它会给「启动 Bundle 内 helper 子进程」增加约束，
        # 而本阶段尚未做正式签名与公证（属发布任务卡范围）。
        "ENABLE_HARDENED_RUNTIME = NO;",
        f"EXECUTABLE_NAME = {PROJECT_NAME};",
        "COMBINE_HIDPI_IMAGES = YES;",
        "CURRENT_PROJECT_VERSION = " + BUILD_NUMBER + ";",
        "GENERATE_INFOPLIST_FILE = NO;",
        f"INFOPLIST_FILE = {PROJECT_NAME}/Info.plist;",
        "LD_RUNPATH_SEARCH_PATHS = (\"$(inherited)\", \"@executable_path/../Frameworks\");",
        f"MARKETING_VERSION = {MARKETING_VERSION};",
        f"PRODUCT_BUNDLE_IDENTIFIER = {BUNDLE_ID};",
        f'PRODUCT_NAME = "{APP_PRODUCT_NAME}";',
        "SWIFT_EMIT_LOC_STRINGS = NO;",
    ]

    def conf(cid: str, name: str, settings: list[str]) -> str:
        body = "\n".join(f"\t\t\t\t{s}" for s in sorted(settings))
        return (f"\t\t{cid} /* {name} */ = {{\n"
                f"\t\t\tisa = XCBuildConfiguration;\n"
                f"\t\t\tbuildSettings = {{\n{body}\n\t\t\t}};\n"
                f"\t\t\tname = {name};\n"
                f"\t\t}};")

    pbx = f"""// !$*UTF8*$!
{{
	archiveVersion = 1;
	classes = {{
	}};
	objectVersion = 56;
	objects = {{

/* Begin PBXBuildFile section */
{chr(10).join(build_files)}
/* End PBXBuildFile section */

/* Begin PBXFileReference section */
{chr(10).join(file_refs)}
/* End PBXFileReference section */

/* Begin PBXFrameworksBuildPhase section */
		{ids['frameworksPhase']} /* Frameworks */ = {{
			isa = PBXFrameworksBuildPhase;
			buildActionMask = 2147483647;
			files = (
			);
			runOnlyForDeploymentPostprocessing = 0;
		}};
/* End PBXFrameworksBuildPhase section */

/* Begin PBXGroup section */
{chr(10).join(groups)}
/* End PBXGroup section */

/* Begin PBXNativeTarget section */
		{ids['target']} /* {PROJECT_NAME} */ = {{
			isa = PBXNativeTarget;
			buildConfigurationList = {ids['targetConfList']} /* Build configuration list for PBXNativeTarget "{PROJECT_NAME}" */;
			buildPhases = (
				{ids['sourcesPhase']} /* Sources */,
				{ids['frameworksPhase']} /* Frameworks */,
				{ids['resourcesPhase']} /* Resources */,
				{ids['helpersPhase']} /* 内置 helper */,
			);
			buildRules = (
			);
			dependencies = (
			);
			name = {PROJECT_NAME};
			productName = "{APP_PRODUCT_NAME}";
			productReference = {ids['product']} /* {APP_PRODUCT_NAME}.app */;
			productType = "com.apple.product-type.application";
		}};
/* End PBXNativeTarget section */

/* Begin PBXProject section */
		{ids['project']} /* Project object */ = {{
			isa = PBXProject;
			attributes = {{
				BuildIndependentTargetsInParallel = 1;
				LastSwiftUpdateCheck = 1600;
				LastUpgradeCheck = 1600;
				TargetAttributes = {{
					{ids['target']} = {{
						CreatedOnToolsVersion = 16.0;
					}};
				}};
			}};
			buildConfigurationList = {ids['projConfList']} /* Build configuration list for PBXProject "{PROJECT_NAME}" */;
			compatibilityVersion = "Xcode 14.0";
			developmentRegion = zh-Hans;
			hasScannedForEncodings = 0;
			knownRegions = (
				zh-Hans,
				Base,
			);
			mainGroup = {ids['rootGroup']};
			productRefGroup = {ids['productsGroup']} /* Products */;
			projectDirPath = "";
			projectRoot = "";
			targets = (
				{ids['target']} /* {PROJECT_NAME} */,
			);
		}};
/* End PBXProject section */

/* Begin PBXResourcesBuildPhase section */
		{ids['resourcesPhase']} /* Resources */ = {{
			isa = PBXResourcesBuildPhase;
			buildActionMask = 2147483647;
			files = (
				{ids['iconBuildFile']} /* {APP_ICON_FILE} in Resources */,
				{ids['menuIconBuildFile']} /* {MENU_ICON_FILE} in Resources */,
			);
			runOnlyForDeploymentPostprocessing = 0;
		}};
/* End PBXResourcesBuildPhase section */

/* Begin PBXShellScriptBuildPhase section */
		{ids['helpersPhase']} /* 内置 helper */ = {{
			isa = PBXShellScriptBuildPhase;
			alwaysOutOfDate = 1;
			buildActionMask = 2147483647;
			files = (
			);
			inputPaths = (
			);
			name = "内置 helper";
			outputPaths = (
			);
			runOnlyForDeploymentPostprocessing = 0;
			shellPath = /bin/sh;
			shellScript = "{esc(HELPERS_SCRIPT)}";
		}};
/* End PBXShellScriptBuildPhase section */

/* Begin PBXSourcesBuildPhase section */
		{ids['sourcesPhase']} /* Sources */ = {{
			isa = PBXSourcesBuildPhase;
			buildActionMask = 2147483647;
			files = (
{source_file_ids}
			);
			runOnlyForDeploymentPostprocessing = 0;
		}};
/* End PBXSourcesBuildPhase section */

/* Begin XCBuildConfiguration section */
{conf(ids['projDebug'], 'Debug', proj_debug)}
{conf(ids['projRelease'], 'Release', proj_release)}
{conf(ids['targetDebug'], 'Debug', target_common)}
{conf(ids['targetRelease'], 'Release', target_common)}
/* End XCBuildConfiguration section */

/* Begin XCConfigurationList section */
		{ids['projConfList']} /* Build configuration list for PBXProject "{PROJECT_NAME}" */ = {{
			isa = XCConfigurationList;
			buildConfigurations = (
				{ids['projDebug']} /* Debug */,
				{ids['projRelease']} /* Release */,
			);
			defaultConfigurationIsVisible = 0;
			defaultConfigurationName = Release;
		}};
		{ids['targetConfList']} /* Build configuration list for PBXNativeTarget "{PROJECT_NAME}" */ = {{
			isa = XCConfigurationList;
			buildConfigurations = (
				{ids['targetDebug']} /* Debug */,
				{ids['targetRelease']} /* Release */,
			);
			defaultConfigurationIsVisible = 0;
			defaultConfigurationName = Release;
		}};
/* End XCConfigurationList section */
	}};
	rootObject = {ids['project']} /* Project object */;
}}
"""

    scheme = f"""<?xml version="1.0" encoding="UTF-8"?>
<Scheme LastUpgradeVersion = "1600" version = "1.7">
   <BuildAction parallelizeBuildables = "YES" buildImplicitDependencies = "YES">
      <BuildActionEntries>
         <BuildActionEntry buildForTesting = "YES" buildForRunning = "YES" buildForProfiling = "YES" buildForArchiving = "YES" buildForAnalyzing = "YES">
            <BuildableReference
               BuildableIdentifier = "primary"
               BlueprintIdentifier = "{ids['target']}"
               BuildableName = "{APP_PRODUCT_NAME}.app"
               BlueprintName = "{PROJECT_NAME}"
               ReferencedContainer = "container:{PROJECT_NAME}.xcodeproj">
            </BuildableReference>
         </BuildActionEntry>
      </BuildActionEntries>
   </BuildAction>
   <TestAction buildConfiguration = "Debug" selectedDebuggerIdentifier = "Xcode.DebuggerFoundation.Debugger.LLDB" selectedLauncherIdentifier = "Xcode.DebuggerFoundation.Launcher.LLDB" shouldUseLaunchSchemeArgsEnv = "YES">
      <Testables>
      </Testables>
   </TestAction>
   <LaunchAction buildConfiguration = "Debug" selectedDebuggerIdentifier = "Xcode.DebuggerFoundation.Debugger.LLDB" selectedLauncherIdentifier = "Xcode.DebuggerFoundation.Launcher.LLDB" launchStyle = "0" useCustomWorkingDirectory = "NO" ignoresPersistentStateOnLaunch = "NO" debugDocumentVersioning = "YES" debugServiceExtension = "internal" allowLocationSimulation = "YES">
      <BuildableProductRunnable runnableDebuggingMode = "0">
         <BuildableReference
            BuildableIdentifier = "primary"
            BlueprintIdentifier = "{ids['target']}"
            BuildableName = "{APP_PRODUCT_NAME}.app"
            BlueprintName = "{PROJECT_NAME}"
            ReferencedContainer = "container:{PROJECT_NAME}.xcodeproj">
         </BuildableReference>
      </BuildableProductRunnable>
   </LaunchAction>
   <ProfileAction buildConfiguration = "Release" shouldUseLaunchSchemeArgsEnv = "YES" savedToolIdentifier = "" useCustomWorkingDirectory = "NO" debugDocumentVersioning = "YES">
      <BuildableProductRunnable runnableDebuggingMode = "0">
         <BuildableReference
            BuildableIdentifier = "primary"
            BlueprintIdentifier = "{ids['target']}"
            BuildableName = "{APP_PRODUCT_NAME}.app"
            BlueprintName = "{PROJECT_NAME}"
            ReferencedContainer = "container:{PROJECT_NAME}.xcodeproj">
         </BuildableReference>
      </BuildableProductRunnable>
   </ProfileAction>
   <AnalyzeAction buildConfiguration = "Debug">
   </AnalyzeAction>
   <ArchiveAction buildConfiguration = "Release" revealArchiveInOrganizer = "YES">
   </ArchiveAction>
</Scheme>
"""

    scheme_dir = proj_dir / "xcshareddata" / "xcschemes"
    scheme_dir.mkdir(parents=True, exist_ok=True)
    (proj_dir / "project.pbxproj").write_text(pbx, encoding="utf-8")
    (scheme_dir / f"{PROJECT_NAME}.xcscheme").write_text(scheme, encoding="utf-8")

    total = sum(len(v) for v in GROUPS.values())
    print(f"已生成 {proj_dir.relative_to(MACOS.parent)}")
    print(f"  源文件 {total} 个（{len(GROUPS)} 个分组）")
    print(f"  scheme {scheme_dir.name}/{PROJECT_NAME}.xcscheme")
    return 0


if __name__ == "__main__":
    sys.exit(main())
