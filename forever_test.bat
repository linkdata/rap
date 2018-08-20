@echo off
:repeat
go test -race
if not errorlevel 1 go test
if not errorlevel 1 goto repeat