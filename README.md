1、执行：cd /data/zlmediakit/zlm-server/ && ./build.zlm.sh 
2、执行：./control.sh zlm update 即可部署完成；
3、访问：http://locahost:7788/ ，登录：amdin + key <cat /data/zlm/cfg/zlm-server.ini |grep secret>

自己部署：
分别编译zlm-server和zlm-client二进制程序，然后修改zlm-client的配置文件，找到对应的zlm-server程序和配置即可；
https://github.com/sunpengfei0307/zlmediakit/blob/main/zlm-client/core/config/config.toml
