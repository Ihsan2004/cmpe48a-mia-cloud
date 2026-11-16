import React from 'react'

const KvkkAydinlatma = () => {
    return (
        <div className="min-h-screen bg-background flex flex-col justify-center py-12 sm:px-6 lg:px-8 font-sans">
            <div className="sm:mx-auto sm:w-full sm:max-w-4xl">
                <h2 className="mt-6 text-center text-3xl text-text font-serif">
                    KVKK Aydınlatma Metni
                </h2>
                <div className="mt-8 bg-surface py-8 px-4 shadow-subtle sm:rounded-lg sm:px-10">
                    <div className="max-w-none text-text-secondary">
                        <p className="text-sm text-text-tertiary mb-4 font-sans">Son Güncelleme: 12 Mayıs 2026 · Sürüm: 1.0</p>

                        <div className="mb-6 border-l-4 border-accent pl-4 py-3 text-text-secondary font-sans text-sm bg-background/40">
                            <strong className="text-text font-medium block mb-1">Beta / PoC Bilgilendirmesi</strong>
                            Syncrun şu anda kapalı beta (Proof of Concept) aşamasındadır ve sınırlı sayıda pilot kullanıcı ile test edilmektedir. Hizmet, kararlı sürüm öncesi olduğundan veri kaybı, hizmet kesintisi ve yapay zekâ kaynaklı hatalı çıktı riski mevcuttur; üretim veya ticari amaçlı kullanım önerilmez. Pilot katılımcılarımızla ayrıca <a href="/pilot-sozlesmesi" className="text-accent hover:text-accent-hover">Pilot Kullanıcı Sözleşmesi</a> imzalanmaktadır.
                        </div>

                        <p className="font-sans">
                            6698 sayılı Kişisel Verilerin Korunması Kanunu ("KVKK") Madde 10 ve Aydınlatma Yükümlülüğünün Yerine Getirilmesinde Uyulacak Usul ve Esaslar Hakkında Tebliğ kapsamında, eşit oranda ortak veri sorumluları sıfatıyla sizleri aşağıdaki şekilde bilgilendiriyoruz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">1. Veri Sorumlularının Kimliği</h3>
                        <p className="font-sans">
                            Hizmet, eşit oranda ve müteselsil olarak sorumlu iki gerçek kişi tarafından yürütülmektedir:
                        </p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li><strong>Faik İhsan Südüpak</strong> — Veri Sorumlusu (Ortak)</li>
                            <li><strong>Kerem Yoldaş</strong> — Veri Sorumlusu (Ortak)</li>
                        </ul>
                        <p className="mt-3 font-sans">
                            <strong>Ortak iletişim e-posta:</strong> support@syncrun.net<br />
                            <strong>KEP:</strong> Yakında eklenecektir<br />
                            <strong>Web:</strong> https://syncrun.net
                        </p>
                        <p className="mt-2 font-sans text-sm text-text-tertiary">
                            Pilot/PoC aşamasında tüzel kişi (şirket) yapısı bulunmadığından MERSİS ve VERBİS kayıt bilgisi yer almamaktadır. Şirket kuruluşunun ardından bu Aydınlatma Metni güncellenecek ve gerekirse VERBİS başvurusu yapılacaktır.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">2. İşlenen Kişisel Veri Kategorileri</h3>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li><strong>Kimlik Bilgisi:</strong> Ad, soyad.</li>
                            <li><strong>İletişim Bilgisi:</strong> E-posta adresi, varsa Slack ID'si.</li>
                            <li><strong>Müşteri İşlem Bilgisi:</strong> Üyelik bilgisi, hesap oluşturma/son giriş zamanı, abonelik durumu, fatura kayıtları.</li>
                            <li><strong>İşlem Güvenliği Bilgisi:</strong> IP adresi, oturum bilgisi, log kayıtları, tarayıcı/cihaz bilgisi, şifre özet (hash) değeri.</li>
                            <li><strong>Görsel ve İşitsel Kayıtlar:</strong> Toplantı ses kaydı (geçici), transkripsiyon metni, konuşmacı segmentleri.</li>
                            <li><strong>Müşteri İçerik Verisi:</strong> Toplantı özetleri, yapay zekâ ile çıkarılmış karar ve aksiyon maddeleri, kullanıcı tarafından eklenen toplantı notları.</li>
                            <li><strong>Pazarlama Verisi:</strong> Açık rıza vermeniz hâlinde, ticari elektronik ileti gönderim kayıtları.</li>
                        </ul>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">3. Kişisel Verilerin İşlenme Amaçları</h3>
                        <p className="font-sans">Kişisel verileriniz aşağıdaki amaçlarla işlenmektedir:</p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li>Üyelik kaydının oluşturulması ve hesap güvenliğinin sağlanması,</li>
                            <li>Hizmet sözleşmesinin kurulması ve ifası — toplantı kaydı, transkripsiyon, özetleme, karar ve aksiyon çıkarımı, toplantı öncesi bilgilendirme,</li>
                            <li>Google Takvim entegrasyonu üzerinden toplantıların senkronizasyonu ve botun otomatik katılımı,</li>
                            <li>Müşteri destek taleplerinin karşılanması,</li>
                            <li>Faturalandırma ve mali yükümlülüklerin yerine getirilmesi,</li>
                            <li>Bilgi güvenliği süreçlerinin yürütülmesi, kötüye kullanımın önlenmesi,</li>
                            <li>Hizmetin geliştirilmesi için anonim/toplulaştırılmış analiz,</li>
                            <li>Açık rızanız hâlinde tanıtım ve pazarlama amaçlı ticari elektronik ileti gönderimi,</li>
                            <li>Yasal yükümlülüklerin ifası ve yetkili kurum taleplerinin karşılanması.</li>
                        </ul>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">4. İşlemenin Hukuki Sebebi (KVKK m.5)</h3>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li><strong>m.5/2-c — Sözleşmenin kurulması veya ifası için zorunlu olması:</strong> Hesap oluşturma, hizmet sunumu, transkripsiyon ve özet üretimi.</li>
                            <li><strong>m.5/2-ç — Hukuki yükümlülüğün yerine getirilmesi:</strong> Vergi, ticaret ve elektronik ticaret mevzuatı kapsamındaki kayıt tutma yükümlülükleri.</li>
                            <li><strong>m.5/2-e — Bir hakkın tesisi, kullanılması veya korunması:</strong> Hukuki uyuşmazlıklarda delil saklama.</li>
                            <li><strong>m.5/2-f — Veri sorumlusunun meşru menfaati:</strong> Sistem güvenliği, dolandırıcılık önleme, hizmet iyileştirme.</li>
                            <li><strong>m.5/1 — Açık rıza:</strong> Toplantı içeriklerinin yapay zekâ ile işlenmesi, yurt dışına aktarım ve ticari elektronik ileti gönderimi.</li>
                        </ul>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">5. Kişisel Verilerin Aktarımı</h3>
                        <p className="font-sans">
                            Kişisel verileriniz, yukarıdaki amaçların gerçekleştirilebilmesi için aşağıdaki taraflara, KVKK m.8 ve m.9 hükümlerine uygun olarak aktarılabilir:
                        </p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li><strong>Yurt İçi:</strong> Yetkili kamu kurum ve kuruluşları (yargı, vergi vb.), hukuki danışmanlar, bağımsız denetçiler, mali müşavirler.</li>
                            <li><strong>Yurt Dışı (Açık Rızanızla):</strong>
                                <ul className="list-disc pl-5 mt-1 space-y-1">
                                    <li>Supabase Inc. (ABD) — veri tabanı ve depolama,</li>
                                    <li>OpenAI, L.L.C. (ABD) — yapay zekâ ile metin işleme,</li>
                                    <li>ElevenLabs, Inc. (ABD) — konuşma-metin dönüşümü,</li>
                                    <li>Google LLC (ABD/AB) — kimlik doğrulama, takvim, e-posta,</li>
                                    <li>Slack Technologies (ABD) — yalnızca aktif Slack entegrasyonunda,</li>
                                    <li>Microsoft Azure (ABD/AB) — toplantı botu iş yürütme.</li>
                                </ul>
                            </li>
                        </ul>
                        <p className="mt-2 font-sans">
                            Yurt dışı aktarımlar, açık rızanız ve KVK Kurulu'nca yayımlanan Standart Sözleşme Hükümleri'ne (SCC) dayalı olarak yapılmaktadır. Açık rızanızı her zaman geri çekebilirsiniz; bu durumda Hizmet'in bazı işlevlerini kullanamayabilirsiniz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">6. Kişisel Veri Toplama Yöntemi</h3>
                        <p className="font-sans">
                            Kişisel verileriniz; web sitemiz, masaüstü uygulamamız, Google OAuth, Slack OAuth, e-posta yazışmaları, çağrı merkezi/destek formları ve Hizmet'in kullanımı sırasında otomatik olarak üretilen log kayıtları aracılığıyla, kısmen otomatik veya tamamen otomatik yöntemlerle toplanmaktadır.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">7. Saklama Süresi</h3>
                        <p className="font-sans">
                            Kişisel verileriniz, ilgili mevzuatın öngördüğü süreler boyunca veya işleme amacının ortadan kalkmasına kadar saklanır. Süreler ve detaylı politika için <a href="/privacy-policy" className="text-accent hover:text-accent-hover">Gizlilik Politikası — Saklama Süreleri</a> bölümünü inceleyiniz. Saklama süresi sonunda kişisel verileriniz silinir, yok edilir veya anonim hâle getirilir.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">8. Haklarınız (KVKK Madde 11)</h3>
                        <p className="font-sans">Veri sahibi olarak şu haklara sahipsiniz:</p>
                        <ol className="list-decimal pl-5 mt-2 space-y-1 font-sans">
                            <li>Kişisel verinizin işlenip işlenmediğini öğrenme,</li>
                            <li>İşlenmişse buna ilişkin bilgi talep etme,</li>
                            <li>İşleme amacını ve amacına uygun kullanılıp kullanılmadığını öğrenme,</li>
                            <li>Yurt içinde/yurt dışında aktarıldığı üçüncü kişileri bilme,</li>
                            <li>Eksik veya yanlış işlenmişse düzeltilmesini isteme,</li>
                            <li>KVKK m.7'de öngörülen şartlar çerçevesinde silinmesini veya yok edilmesini isteme,</li>
                            <li>5 ve 6. maddeler uyarınca yapılan işlemlerin, verilerin aktarıldığı üçüncü kişilere bildirilmesini isteme,</li>
                            <li>Münhasıran otomatik sistemlerle analiz edilmesi suretiyle aleyhinize bir sonuç çıkmasına itiraz etme,</li>
                            <li>Kanuna aykırı işleme sebebiyle zarara uğramanız hâlinde zararın giderilmesini talep etme.</li>
                        </ol>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">9. Başvuru Yöntemi</h3>
                        <p className="font-sans">
                            KVKK m.13 ve Veri Sorumlusuna Başvuru Usul ve Esasları Hakkında Tebliğ uyarınca başvurularınızı şu yollarla iletebilirsiniz:
                        </p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li><strong>E-posta:</strong> support@syncrun.net (sistemde kayıtlı e-posta adresinizden),</li>
                            <li><strong>KEP:</strong> Yakında eklenecektir,</li>
                            <li><strong>Hesap Paneli:</strong> Ayarlar &rarr; Gizlilik &rarr; Veri Talebi (yakında eklenecektir).</li>
                        </ul>
                        <p className="mt-2 font-sans">
                            Başvurunuzun yanıtlanması için kimliğinizi doğrulayıcı bilgi sunmanız gerekmektedir. Talebiniz, niteliğine göre en geç <strong>30 (otuz) gün</strong> içinde ücretsiz olarak sonuçlandırılır. Cevabımızdan memnun kalmamanız hâlinde, cevabımızı öğrendiğiniz tarihten itibaren <strong>30 gün</strong> ve her hâlükârda başvuru tarihinden itibaren <strong>60 gün</strong> içinde Kişisel Verileri Koruma Kurulu'na şikâyette bulunabilirsiniz.
                        </p>

                        <p className="mt-6 font-sans text-sm text-text-tertiary">
                            Bu Aydınlatma Metni'nin güncel sürümüne <a href="/kvkk" className="text-accent hover:text-accent-hover">syncrun.net/kvkk</a> adresinden ulaşabilirsiniz.
                        </p>
                    </div>
                </div>
            </div>
        </div>
    )
}

export default KvkkAydinlatma
