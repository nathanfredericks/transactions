// Exercise the original pinned wrapper on the same local, non-secret fixture.
import {createServer} from 'node:http';
import {readFile} from 'node:fs/promises';
import {launchContext} from '/tmp/browser-install/node_modules/cloakbrowser/dist/index.js';
const body=await readFile('scripts/cloak/inspection.html');
const server=createServer((req,res)=>{res.setHeader('content-type','text/html; charset=utf-8');res.end(req.url==='/frame'?'<!doctype html><html lang="en"><label for="child">Child text</label><input id="child" name="child"></html>':body)});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const context=await launchContext({headless:false,humanize:true,humanPreset:'careful',geoip:true,timezone:'America/Halifax'});
try{
 const page=await context.newPage();await page.goto(`http://127.0.0.1:${server.address().port}`);
 await page.locator('#text').fill('Ab9@!_é🙂');await page.locator('#action').click();await page.locator('#check').check();await page.locator('#far').fill('scroll');
 const report=await page.evaluate(()=>window.report());
 if(report.text!=='Ab9@!_é🙂'||report.far!=='scroll'||!report.checked||report.clicks!==1||report.events.some(e=>!e.trusted)||report.pressed.length)throw new Error('reference inspection differs');
 console.log(JSON.stringify(report));
}finally{await context.close();await new Promise(resolve=>server.close(resolve))}
