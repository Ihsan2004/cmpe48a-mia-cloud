import React from 'react'

const PrivacyPolicy = () => {
    return (
        <div className="min-h-screen bg-background flex flex-col justify-center py-12 sm:px-6 lg:px-8 font-sans">
            <div className="sm:mx-auto sm:w-full sm:max-w-4xl">
                <h2 className="mt-6 text-center text-3xl text-text font-serif">
                    Gizlilik Politikası
                </h2>
                <div className="mt-8 bg-surface py-8 px-4 shadow-subtle sm:rounded-lg sm:px-10">
                    <div className="max-w-none text-text-secondary">
                        <p className="text-sm text-text-tertiary mb-4 font-sans">Son Güncelleme: 12 Mayıs 2026 · Sürüm: 1.0</p>

                        <div className="mb-6 border-l-4 border-accent pl-4 py-3 text-text-secondary font-sans text-sm bg-background/40">
                            <strong className="text-text font-medium block mb-1">Beta / PoC Bilgilendirmesi</strong>
                            Syncrun şu anda kapalı beta (Proof of Concept) aşamasındadır ve sınırlı sayıda pilot kullanıcı ile test edilmektedir. Hizmet, kararlı sürüm öncesi olduğundan veri kaybı, hizmet kesintisi ve yapay zekâ kaynaklı hatalı çıktı riski mevcuttur; üretim veya ticari amaçlı kullanım önerilmez. Pilot katılımcılarımızla ayrıca <a href="/pilot-sozlesmesi" className="text-accent hover:text-accent-hover">Pilot Kullanıcı Sözleşmesi</a> imzalanmaktadır.
                        </div>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">1. Giriş</h3>
                        <p className="font-sans">
                            Syncrun adı altında pilot hizmet sunan eşit oranda ortak veri sorumluları <strong>Faik İhsan Südüpak</strong> ve <strong>Kerem Yoldaş</strong> (birlikte "Syncrun", "biz") olarak gizliliğinize önem veriyoruz. Bu Gizlilik Politikası; Syncrun Web Paneli, Masaüstü Kaydedici, toplantı botu ve ilgili API'ler (birlikte "Hizmet") aracılığıyla hangi kişisel verileri topladığımızı, neden topladığımızı, nasıl kullandığımızı, kimlerle paylaştığımızı ve haklarınızı açıklar.
                        </p>
                        <p className="mt-2 font-sans">
                            Bu metin; 6698 sayılı Kişisel Verilerin Korunması Kanunu (KVKK) ve Avrupa Birliği Genel Veri Koruma Tüzüğü (GDPR) ile uyumlu olarak hazırlanmıştır. KVKK'ya özgü detaylı bilgilendirme için <a href="/kvkk" className="text-accent hover:text-accent-hover">KVKK Aydınlatma Metni</a>'mizi okuyunuz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">2. Veri Sorumluları</h3>
                        <p className="font-sans">
                            Hizmet, eşit oranda ortak veri sorumluları tarafından yürütülmektedir:<br />
                            &bull; <strong>Faik İhsan Südüpak</strong><br />
                            &bull; <strong>Kerem Yoldaş</strong><br />
                            <strong>Ortak iletişim:</strong> support@syncrun.net<br />
                            <strong>KEP:</strong> Yakında eklenecektir
                        </p>
                        <p className="mt-2 font-sans text-sm text-text-tertiary">
                            Veri sorumluları KVKK ve GDPR kapsamında eşit ve müteselsil olarak sorumludur. PoC döneminde tüzel kişi (şirket) yapısı kurulduğunda bu metin güncellenecektir.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">3. İşlediğimiz Kişisel Veriler</h3>
                        <p className="font-sans">Hizmet kapsamında aşağıdaki veri kategorilerini işliyoruz:</p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li><strong>Kimlik ve İletişim:</strong> Ad, soyad, e-posta adresi, profil fotoğrafı (Google'dan).</li>
                            <li><strong>Hesap Verisi:</strong> Şifre (yalnızca hashlenmiş biçimde — bcrypt), hesap oluşturma tarihi, son giriş zamanı.</li>
                            <li><strong>Google Takvim Verisi:</strong> Toplantı başlığı, başlangıç/bitiş saati, açıklama, katılımcı listesi, toplantı bağlantısı, yinelenen toplantı serisi. (OAuth ile yalnızca okuma izniyle erişiriz.)</li>
                            <li><strong>Toplantı İçerik Verisi:</strong> Toplantı ses kaydı (geçici), transkripsiyon metni, konuşmacı segmentleri, otomatik üretilmiş özet, çıkarılan kararlar ve aksiyon maddeleri, kullanıcının yazdığı kişisel notlar.</li>
                            <li><strong>Entegrasyon Verisi:</strong> Slack workspace ID, kanal/kullanıcı ID, OAuth token (şifreli saklanır).</li>
                            <li><strong>Teknik ve Kullanım Verisi:</strong> IP adresi, tarayıcı/cihaz bilgisi, oturum ID, hata kayıtları, özellik kullanım metrikleri.</li>
                            <li><strong>İletişim Verisi:</strong> Destek talepleri, geri bildirimler.</li>
                        </ul>
                        <p className="mt-2 font-sans">
                            <strong>Özel nitelikli kişisel veri</strong> toplamayız. Toplantı kayıtlarında bu tür veriler geçerse, bunlar açık rızanızla işlenir ve aynı koruma seviyesinde saklanır.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">4. İşleme Amaçları ve Hukuki Sebep</h3>
                        <div className="overflow-x-auto mt-2">
                            <table className="w-full text-sm font-sans border-collapse">
                                <thead>
                                    <tr className="border-b border-text-tertiary/30">
                                        <th className="text-left py-2 pr-3 text-text font-medium">Amaç</th>
                                        <th className="text-left py-2 pr-3 text-text font-medium">KVKK m.5 Hukuki Sebep</th>
                                        <th className="text-left py-2 text-text font-medium">GDPR Art. 6 Hukuki Sebep</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    <tr className="border-b border-text-tertiary/10">
                                        <td className="py-2 pr-3 align-top">Hesap oluşturma, kimlik doğrulama, sözleşmenin ifası</td>
                                        <td className="py-2 pr-3 align-top">Sözleşmenin kurulması/ifası (m.5/2-c)</td>
                                        <td className="py-2 align-top">Sözleşmenin ifası (Art.6/1-b)</td>
                                    </tr>
                                    <tr className="border-b border-text-tertiary/10">
                                        <td className="py-2 pr-3 align-top">Toplantı ses kaydı, transkripsiyon, özet, karar çıkarımı</td>
                                        <td className="py-2 pr-3 align-top">Açık rıza (m.5/1) ve sözleşmenin ifası</td>
                                        <td className="py-2 align-top">Açık rıza (Art.6/1-a) ve sözleşme (Art.6/1-b)</td>
                                    </tr>
                                    <tr className="border-b border-text-tertiary/10">
                                        <td className="py-2 pr-3 align-top">Hizmet güvenliğini sağlama, kötüye kullanımı önleme</td>
                                        <td className="py-2 pr-3 align-top">Meşru menfaat (m.5/2-f)</td>
                                        <td className="py-2 align-top">Meşru menfaat (Art.6/1-f)</td>
                                    </tr>
                                    <tr className="border-b border-text-tertiary/10">
                                        <td className="py-2 pr-3 align-top">Yasal yükümlülüklerin yerine getirilmesi (faturalama vb.)</td>
                                        <td className="py-2 pr-3 align-top">Hukuki yükümlülük (m.5/2-ç)</td>
                                        <td className="py-2 align-top">Hukuki yükümlülük (Art.6/1-c)</td>
                                    </tr>
                                    <tr className="border-b border-text-tertiary/10">
                                        <td className="py-2 pr-3 align-top">Ürün iyileştirme, anonim analiz</td>
                                        <td className="py-2 pr-3 align-top">Meşru menfaat (m.5/2-f)</td>
                                        <td className="py-2 align-top">Meşru menfaat (Art.6/1-f)</td>
                                    </tr>
                                    <tr>
                                        <td className="py-2 pr-3 align-top">Ticari elektronik ileti (pazarlama)</td>
                                        <td className="py-2 pr-3 align-top">Açık rıza (m.5/1)</td>
                                        <td className="py-2 align-top">Açık rıza (Art.6/1-a)</td>
                                    </tr>
                                </tbody>
                            </table>
                        </div>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">5. Aktardığımız Taraflar ve Yurt Dışı Aktarım</h3>
                        <p className="font-sans">Hizmet'i sunabilmek için aşağıdaki veri işleyenlerle çalışıyoruz:</p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li><strong>Supabase Inc. (ABD/AB)</strong> — Veri tabanı barındırma ve nesne depolama. Toplantı transkriptleri, özetler ve hesap verisi burada saklanır.</li>
                            <li><strong>OpenAI, L.L.C. (ABD)</strong> — Transkript düzeltme, özetleme ve karar/aksiyon çıkarımı için metin işleme. API üzerinden gönderilen veriler OpenAI tarafından model eğitiminde kullanılmaz (API politikası gereği).</li>
                            <li><strong>ElevenLabs, Inc. (ABD)</strong> — Ses dosyalarının metne dönüştürülmesi (Scribe v2).</li>
                            <li><strong>Google LLC (ABD/AB)</strong> — OAuth kimlik doğrulama, Google Takvim okuma, Google Workspace SMTP üzerinden e-posta gönderimi.</li>
                            <li><strong>Slack Technologies (ABD)</strong> — Yalnızca Slack entegrasyonunu etkinleştirdiyseniz, brief mesajlarının iletilmesi için.</li>
                            <li><strong>Microsoft Azure (Container Apps, ABD/AB)</strong> — Toplantı botu işlerinin çalıştırılması.</li>
                            <li><strong>Yetkili kamu kurum ve kuruluşları</strong> — Hukuki yükümlülük halinde, talep edilen kapsamla sınırlı olarak.</li>
                        </ul>
                        <p className="mt-2 font-sans">
                            <strong>Yurt Dışı Aktarım:</strong> Yukarıdaki servis sağlayıcılarının önemli bir kısmı verilerinizi ABD'deki sunucularda işleyebilir. KVKK m.9 kapsamında bu aktarım, <strong>açık rızanıza</strong> ve servis sağlayıcılarla imzalanan Veri İşleme Sözleşmeleri (DPA) ile Standart Sözleşme Hükümleri'ne (SCC) dayanır.
                        </p>
                        <p className="mt-2 font-sans">
                            <strong>Google API Limited Use:</strong> Google API'lerinden elde edilen verilerin kullanımı ve aktarımı <a href="https://developers.google.com/terms/api-services-user-data-policy#additional_requirements_for_specific_api_scopes" target="_blank" rel="noopener noreferrer" className="text-accent hover:text-accent-hover">Google API Services User Data Policy</a> ile uyumludur. Bu veriler genel amaçlı yapay zekâ/makine öğrenmesi modellerinin eğitiminde kullanılmaz, satılmaz veya reklam amacıyla işlenmez.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">6. Saklama Süreleri</h3>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li><strong>Hesap verisi:</strong> Hesabınız aktif olduğu sürece + hesap silme talebinden sonra <strong>30 gün</strong> (yedekten silme).</li>
                            <li><strong>Toplantı ses kaydı:</strong> Başarılı transkripsiyondan hemen sonra silinir; başarısız işlerde en fazla <strong>7 gün</strong>.</li>
                            <li><strong>Transkript, özet, kararlar, notlar:</strong> Siz silene veya hesabınızı kapatana kadar.</li>
                            <li><strong>Fatura ve mali kayıtlar:</strong> Vergi Usul Kanunu uyarınca <strong>10 yıl</strong>.</li>
                            <li><strong>Hukuki uyuşmazlıklar:</strong> Türk Borçlar Kanunu'na göre <strong>10 yıl</strong> zamanaşımı süresince.</li>
                            <li><strong>Anonim analitik veri:</strong> Süresiz (kişisel veri niteliği yoktur).</li>
                        </ul>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">7. Veri Güvenliği</h3>
                        <p className="font-sans">Endüstri standardı önlemler uygularız:</p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li>Tüm trafikte TLS 1.2+ şifreleme,</li>
                            <li>Depolanan OAuth token'ların ve hassas alanların AES-256 ile şifrelenmesi,</li>
                            <li>Şifrelerin bcrypt ile hashlenmesi (asla düz metin saklanmaz),</li>
                            <li>Veri tabanı seviyesinde Row Level Security (RLS) ile kullanıcı izolasyonu,</li>
                            <li>Erişim kayıtları ve düzenli güvenlik denetimleri,</li>
                            <li>En az ayrıcalık prensibi ve iki faktörlü kimlik doğrulama gereksinimi (çalışan erişimi için).</li>
                        </ul>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">8. Çerezler ve Yerel Depolama</h3>
                        <p className="font-sans">
                            Oturum yönetimi ve tercihleriniz için zorunlu çerezler ve <code>localStorage</code> kullanırız. Üçüncü taraf reklam veya izleme çerezi kullanmıyoruz. Tarayıcı ayarlarınızdan çerezleri silebilirsiniz; bu durumda oturum açık kalmayabilir.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">9. Haklarınız</h3>
                        <p className="font-sans"><strong>KVKK Madde 11 ve GDPR Article 15-22 kapsamında şu haklara sahipsiniz:</strong></p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li>Kişisel verilerinizin işlenip işlenmediğini öğrenme,</li>
                            <li>İşlenmişse buna ilişkin bilgi talep etme,</li>
                            <li>İşleme amacını ve amacına uygun kullanılıp kullanılmadığını öğrenme,</li>
                            <li>Yurt içinde/yurt dışında aktarıldığı üçüncü kişileri bilme,</li>
                            <li>Eksik veya yanlış işlenmişse düzeltilmesini isteme,</li>
                            <li>KVKK m.7 / GDPR Art.17 kapsamında silme veya yok edilmesini isteme,</li>
                            <li>Aktarıldığı üçüncü kişilere yapılan işlemlerin bildirilmesini isteme,</li>
                            <li>Otomatik analiz sonucunda aleyhe çıkan sonuçlara itiraz etme,</li>
                            <li>Kanuna aykırı işleme sebebiyle zarara uğramışsanız tazminat talep etme,</li>
                            <li>Verilerinizi yapılandırılmış ve makine okunabilir bir formatta alma (taşınabilirlik — GDPR Art.20),</li>
                            <li>Açık rızanızı her zaman geri çekme.</li>
                        </ul>
                        <p className="mt-2 font-sans">
                            Bu hakların kullanılmasına ilişkin başvuru usul ve esasları <a href="/kvkk" className="text-accent hover:text-accent-hover">KVKK Aydınlatma Metni</a>'nde detaylı olarak belirtilmiştir. Başvurular en geç <strong>30 gün</strong> içinde ücretsiz olarak yanıtlanır. KVK Kurulu'na şikâyet etme hakkınız saklıdır.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">10. Çocukların Gizliliği</h3>
                        <p className="font-sans">
                            Hizmet, 18 yaşının altındaki kişilere yönelik değildir. 18 yaş altında olduğunu öğrendiğimiz bir kullanıcının hesabını derhal kapatır ve verilerini sileriz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">11. Politika Değişiklikleri</h3>
                        <p className="font-sans">
                            Bu politikayı zaman zaman güncelleyebiliriz. Önemli değişikliklerde sizi e-posta ile veya panel üzerinden bilgilendiririz. Güncelleme tarihini her zaman bu sayfanın başında görebilirsiniz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">12. İletişim</h3>
                        <p className="font-sans">
                            Sorularınız ve KVKK kapsamındaki başvurularınız için:<br />
                            <strong>E-posta:</strong> support@syncrun.net<br />
                            <strong>KEP:</strong> Yakında eklenecektir<br />
                            <strong>Veri Sorumluları:</strong> Faik İhsan Südüpak, Kerem Yoldaş
                        </p>
                    </div>
                </div>
            </div>
        </div>
    )
}

export default PrivacyPolicy
