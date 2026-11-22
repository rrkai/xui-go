#!/bin/bash

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

cur_dir=$(pwd)
fresh_install=0

# check root
[[ $EUID -ne 0 ]] && echo -e "${red}错误：${plain}请使用 root 用户运行此脚本 \n " && exit 1

# Check OS and set release variable
if [[ -f /etc/os-release ]]; then
    source /etc/os-release
    release=$ID
elif [[ -f /usr/lib/os-release ]]; then
    source /usr/lib/os-release
    release=$ID
else
    echo "检测系统发行版失败，请联系作者！" >&2
    exit 1
fi
echo "当前系统发行版：$release"

arch() {
    case "$(uname -m)" in
    x86_64 | x64 | amd64) echo 'amd64' ;;
    i*86 | x86) echo '386' ;;
    armv8* | armv8 | arm64 | aarch64) echo 'arm64' ;;
    armv7* | armv7 | arm) echo 'armv7' ;;
    armv6* | armv6) echo 'armv6' ;;
    armv5* | armv5) echo 'armv5' ;;
    s390x) echo 's390x' ;;
    *) echo -e "${green}不支持的 CPU 架构！${plain}" && rm -f install.sh && exit 1 ;;
    esac
}

echo "CPU 架构：$(arch)"

install_dependencies() {
    case "${release}" in
    ubuntu | debian | armbian)
        apt-get update && apt-get install -y -q wget curl tar tzdata cron ca-certificates nftables
        ;;
    centos | almalinux | rocky | ol)
        yum -y update && yum install -y -q wget curl tar tzdata cronie ca-certificates nftables
        ;;
    fedora | amzn)
        dnf -y update && dnf install -y -q wget curl tar tzdata cronie ca-certificates nftables
        ;;
    arch | manjaro | parch)
        pacman -Syu && pacman -Syu --noconfirm wget curl tar tzdata cronie ca-certificates nftables
        ;;
    opensuse-tumbleweed)
        zypper refresh && zypper -q install -y wget curl tar timezone cron ca-certificates nftables
        ;;
    *)
        apt-get update && apt install -y -q wget curl tar tzdata cron ca-certificates nftables
        ;;
    esac
}

gen_random_string() {
    local length="$1"
    local random_string=$(LC_ALL=C tr -dc 'a-zA-Z0-9' </dev/urandom | fold -w "$length" | head -n 1)
    echo "$random_string"
}

gen_random_web_base_path() {
    local digits lower upper combined
    digits=$(head -c 512 /dev/urandom | LC_ALL=C tr -dc '2-9')
    lower=$(head -c 512 /dev/urandom | LC_ALL=C tr -dc 'abcdefghjkmnpqrstuvwxyz')
    upper=$(head -c 512 /dev/urandom | LC_ALL=C tr -dc 'ABCDEFGHJKMNPQRSTUVWXYZ')
    combined="${digits:0:2}${lower:0:3}${upper:0:4}"
    echo "$combined" | fold -w1 | shuf | tr -d '\n'
    echo
}

config_after_install() {
    local existing_username=$(/usr/local/x-ui/x-ui setting -show true | grep -Eo 'username: .+' | awk '{print $2}')
    local existing_password=$(/usr/local/x-ui/x-ui setting -show true | grep -Eo 'password: .+' | awk '{print $2}')
    local existing_webBasePath=$(/usr/local/x-ui/x-ui setting -show true | grep -Eo 'webBasePath: .+' | awk '{print $2}')

    if [[ ${#existing_webBasePath} -lt 4 ]]; then
        if [[ "$existing_username" == "admin" && "$existing_password" == "admin" ]]; then
            local config_webBasePath=$(gen_random_web_base_path)
            local config_username=$(gen_random_string 10)
            local config_password=$(gen_random_string 10)

            local config_port=$(shuf -i 50000-59999 -n 1)
            fresh_install=1
            /usr/local/x-ui/x-ui setting -username "${config_username}" -password "${config_password}" -port "${config_port}" -webBasePath "${config_webBasePath}"
            echo -e "${yellow}检测到全新安装，已生成随机登录信息${plain}"
        else
            local config_webBasePath=$(gen_random_web_base_path)
            echo -e "${yellow}面板根路径缺失或过短，正在重新生成……${plain}"
            /usr/local/x-ui/x-ui setting -webBasePath "${config_webBasePath}"
        fi
    else
        if [[ "$existing_username" == "admin" && "$existing_password" == "admin" ]]; then
            local config_username=$(gen_random_string 10)
            local config_password=$(gen_random_string 10)

            echo -e "${yellow}检测到默认用户名和密码，正在更新以保障安全……${plain}"
            /usr/local/x-ui/x-ui setting -username "${config_username}" -password "${config_password}"
        fi
    fi

    /usr/local/x-ui/x-ui migrate
}

install_x-ui() {
    # checks if the installation backup dir exist. if existed then ask user if they want to restore it else continue installation.
    if [[ -e /usr/local/x-ui-backup/ ]]; then
        read -p "检测到上次安装未完成，是否恢复之前已安装的版本？[y/n]：" restore_confirm
        if [[ "${restore_confirm}" == "y" || "${restore_confirm}" == "Y" ]]; then
            systemctl stop x-ui
            mv /usr/local/x-ui-backup/x-ui.db /etc/x-ui/ -f
            mv /usr/local/x-ui-backup/ /usr/local/x-ui/ -f
            systemctl start x-ui
            echo -e "${green}之前安装的 x-ui 已恢复${plain}，服务正在运行……"
            exit 0
        else
            echo -e "继续安装 x-ui……"
        fi
    fi

    cd /usr/local/

    if [ $# == 0 ]; then
        last_version=$(curl -Ls "https://api.github.com/repos/rrkai/xui-go/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
        if [[ ! -n "$last_version" ]]; then
            echo -e "${red}获取 x-ui 版本失败，可能是 GitHub API 限制，请稍后重试${plain}"
            exit 1
        fi
        echo -e "获取到 x-ui 最新版本：${last_version}，开始安装……"
        wget -N --no-check-certificate -O /usr/local/x-ui-linux-$(arch).tar.gz https://github.com/rrkai/xui-go/releases/download/${last_version}/x-ui-linux-$(arch).tar.gz
        if [[ $? -ne 0 ]]; then
            echo -e "${red}下载 x-ui 失败，请确认服务器可以访问 GitHub${plain}"
            exit 1
        fi
    else
        last_version=$1
        url="https://github.com/rrkai/xui-go/releases/download/${last_version}/x-ui-linux-$(arch).tar.gz"
        echo -e "开始安装 x-ui $1"
        wget -N --no-check-certificate -O /usr/local/x-ui-linux-$(arch).tar.gz ${url}
        if [[ $? -ne 0 ]]; then
            echo -e "${red}下载 x-ui $1 失败，请确认该版本是否存在${plain}"
            exit 1
        fi
    fi

    if [[ -e /usr/local/x-ui/ ]]; then
        systemctl stop x-ui
        # keep the panel down long enough for open pages to notice the restart during unattended updates
        if [[ "${XUI_ONLINE_UPDATE}" == "1" || ! -t 0 ]]; then
            sleep 4
        fi
        mv /usr/local/x-ui/ /usr/local/x-ui-backup/ -f
        cp /etc/x-ui/x-ui.db /usr/local/x-ui-backup/ -f
    fi

    tar zxvf x-ui-linux-$(arch).tar.gz
    rm x-ui-linux-$(arch).tar.gz -f
    cd x-ui
    chmod +x x-ui

    # Check the system's architecture and rename the file accordingly
    if [[ $(arch) == "armv7" ]]; then
        mv bin/xray-linux-$(arch) bin/xray-linux-arm
        chmod +x bin/xray-linux-arm
    fi
    chmod +x x-ui bin/xray-linux-$(arch)
    cp -f x-ui.service /etc/systemd/system/
    # raw.githubusercontent.com may be unavailable, fall back to the GitHub API
    if ! wget --no-check-certificate -q -O /usr/bin/x-ui https://raw.githubusercontent.com/rrkai/xui-go/main/x-ui.sh || ! head -1 /usr/bin/x-ui | grep -q '^#!/bin/bash'; then
        curl -fsSL -H "Accept: application/vnd.github.raw" "https://api.github.com/repos/rrkai/xui-go/contents/x-ui.sh?ref=main" -o /usr/bin/x-ui && head -1 /usr/bin/x-ui | grep -q '^#!/bin/bash' || {
            echo -e "${red}下载 x-ui 管理脚本失败，请检查网络后重试${plain}"
            exit 1
        }
    fi
    chmod +x /usr/local/x-ui/x-ui.sh
    chmod +x /usr/bin/x-ui
    config_after_install
    if [[ "${fresh_install}" == "1" ]]; then
        echo -e "${yellow}正在自动申请面板 IP 证书（Let's Encrypt，6 天有效，到期自动续期）...${plain}"
        if timeout 120 /usr/local/x-ui/x-ui autocert >/tmp/xui-autocert.log 2>&1; then
            echo -e "${green}IP 证书申请成功，面板将以 HTTPS 启动${plain}"
        else
            echo -e "${yellow}IP 证书申请未成功，面板先以 HTTP 启动；请确认公网 80 端口可用（NAT 机型需先映射），或在“面板设置 - 面板安全”中重试${plain}"
            tail -n 3 /tmp/xui-autocert.log 2>/dev/null | sed 's/^/  /'
        fi
    fi
    rm /usr/local/x-ui-backup/ -rf
    #echo -e "If it is a new installation, the default web port is ${green}54321${plain}, The username and password are ${green}admin${plain} by default"
    #echo -e "Please make sure that this port is not occupied by other procedures,${yellow} And make sure that port 54321 has been released${plain}"
    #    echo -e "If you want to modify the 54321 to other ports and enter the x-ui command to modify it, you must also ensure that the port you modify is also released"
    #echo -e ""
    #echo -e "If it is updated panel, access the panel in your previous way"
    #echo -e ""
    systemctl daemon-reload
    systemctl enable x-ui
    systemctl start x-ui
    echo -e "${green}x-ui ${last_version}${plain} 安装完成，服务正在运行……"
    echo -e ""
    panel_settings=$(/usr/local/x-ui/x-ui setting -show true 2>/dev/null || true)
    panel_username=$(printf '%s\n' "$panel_settings" | sed -n 's/^username: //p' | head -1)
    panel_password=$(printf '%s\n' "$panel_settings" | sed -n 's/^password: //p' | head -1)
    panel_port=$(printf '%s\n' "$panel_settings" | sed -n 's/^port: //p' | head -1)
    panel_webBasePath=$(printf '%s\n' "$panel_settings" | sed -n 's/^webBasePath: //p' | head -1)
    echo -e "${green}面板登录信息${plain}"
    echo "------------------------------------------"
    echo -e "用户名：${green}${panel_username}${plain}"
    echo -e "密码：${green}${panel_password}${plain}"
    echo -e "端口：${green}${panel_port}${plain}"
    echo -e "面板根路径：${green}${panel_webBasePath}${plain}"
    echo "------------------------------------------"
    echo -e "${yellow}如果忘记登录信息，可执行 'x-ui settings' 查看${plain}"
    echo -e ""
    echo -e "可通过以下地址访问面板：${yellow}"
    /usr/local/x-ui/x-ui uri
    echo -e "${plain}"
    echo "X-UI 控制菜单用法"
    echo "------------------------------------------"
    echo "子命令："
    echo "x-ui              - 管理脚本"
    echo "x-ui start        - 启动"
    echo "x-ui stop         - 停止"
    echo "x-ui restart      - 重启"
    echo "x-ui status       - 查看当前状态"
    echo "x-ui settings     - 查看当前设置"
    echo "x-ui enable       - 设置开机自启"
    echo "x-ui disable      - 取消开机自启"
    echo "x-ui log          - 查看日志"
    echo "x-ui update       - 更新"
    echo "x-ui install      - 安装"
    echo "x-ui uninstall    - 卸载"
    echo "x-ui help         - 查看菜单用法"
    echo "------------------------------------------"
}

echo -e "${green}正在运行……${plain}"
install_dependencies
install_x-ui $1
