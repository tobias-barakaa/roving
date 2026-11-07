// DEMONSTRATION DATA ONLY. Replace with real API responses (see lib/api/).
export interface Group{name:string;subject:string;description:string;members:number;visibility:'Public'|'Private';hue:number;slug:string}
export interface Book{title:string;subject:string;category:string;hue:number;slug:string}
export interface Session{title:string;kind:string;when:string;detail:string}
export const categories=['Technology','Science','Mathematics','Literature','Business','Philosophy','Psychology','History','Languages','Arts','Education','Health'].map((n,i)=>({name:n,count:[84,63,41,72,38,29,35,57,46,33,52,27][i],slug:n.toLowerCase()}));
export const groups:Group[]=[
{name:'Systems Programming Circle',subject:'Technology',description:'Weekly working sessions on Go, Rust and operating systems.',members:248,visibility:'Public',hue:210,slug:'systems-programming'},
{name:'Proof Writers',subject:'Mathematics',description:'Practice constructing and critiquing proofs together.',members:131,visibility:'Public',hue:260,slug:'proof-writers'},
{name:'Slow Readers Society',subject:'Literature',description:'One novel a month, read at a steady shared pace.',members:402,visibility:'Public',hue:20,slug:'slow-readers'},
{name:'Exam Season Study Hall',subject:'Education',description:'Quiet rooms and accountability for revision periods.',members:96,visibility:'Private',hue:150,slug:'exam-hall'}];
export const books:Book[]=[
{title:'Foundations of Computation',subject:'Technology',category:'Textbook',hue:210,slug:'foundations-computation'},
{title:'A Field Guide to Calculus',subject:'Mathematics',category:'Study guide',hue:260,slug:'calculus-field-guide'},
{title:'Ethics and the Examined Life',subject:'Philosophy',category:'Essay collection',hue:340,slug:'ethics-examined-life'},
{title:'Cells, Systems, Organisms',subject:'Science',category:'Textbook',hue:150,slug:'cells-systems'},
{title:'Writing the Short Story',subject:'Literature',category:'Craft',hue:20,slug:'short-story'},
{title:'Europe Between Wars',subject:'History',category:'Monograph',hue:45,slug:'europe-between-wars'}];
export const sessions:Session[]=[
{title:'Group study',kind:'Scheduled',when:'Weekdays, 18:00',detail:'Join a booked session with your group, with attendance tracked for you.'},
{title:'Pomodoro focus',kind:'Timed',when:'25 / 5 minute cycles',detail:'Work in shared focus blocks and take breaks together.'},
{title:'Study rooms',kind:'Open rooms',when:'Always open',detail:'Public or private rooms with chat, audio and video.'}];
export const activities=[
{title:'Discussions',detail:'Group messaging and threads on what you are reading.'},
{title:'Collaboration',detail:'Shared notes and reading progress across a group.'},
{title:'Chess',detail:'A recreational game between study blocks.'}];
