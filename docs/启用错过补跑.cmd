@echo off
chcp 65001 >nul
setlocal

rem ---------------------------------------------------------------
rem  为 cne 的两个计划任务打开「错过计划开始时间后尽快启动」。
rem
rem  为什么要它：两个任务现在是「启用了、时间也对，但错过就跳过」——
rem  17:30 / 21:00 时电脑关着或休眠，那天会被直接跳过，而任务列表看不出异常。
rem
rem  为什么要有这个脚本：freeStock.exe 的进程里 COM 激活失败
rem  （schtasks /query 报「系统找不到指定的路径」，而 schtasks /? 正常），
rem  所以应用内改不了。这个脚本在普通命令行里跑，不受那个限制。
rem
rem  用法：双击即可，不需要管理员权限。
rem ---------------------------------------------------------------

echo 当前两个任务：
schtasks /query /tn cne-daily-update /fo LIST | findstr /i "TaskName Next"
schtasks /query /tn cne-daily-stale  /fo LIST | findstr /i "TaskName Next"
echo.

echo 正在打开「错过也补跑」...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$n='cne-daily-update','cne-daily-stale'; foreach($t in $n){ $x=Get-ScheduledTask -TaskName $t; $s=$x.Settings; $s.StartWhenAvailable=$true; $s.DisallowStartIfOnBatteries=$false; $s.StopIfGoingOnBatteries=$false; Set-ScheduledTask -TaskName $t -Settings $s | Out-Null; Write-Host ('  已设置 ' + $t) }"
if errorlevel 1 (
  echo.
  echo [失败] 设置没成功。请把上面的报错发出来。
  pause
  exit /b 1
)

echo.
echo 验证结果：
powershell -NoProfile -Command "$n='cne-daily-update','cne-daily-stale'; foreach($t in $n){ $x=Get-ScheduledTask -TaskName $t; Write-Host ('  ' + $t + '  StartWhenAvailable=' + $x.Settings.StartWhenAvailable + '  State=' + $x.State) }"

echo.
echo 完成。之后 17:30 / 21:00 如果错过，会在机器可用时补跑。
pause
